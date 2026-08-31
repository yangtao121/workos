package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Installation invariants live in this module's domain: the registry keeps
// immutable manifest facts, the project keeps install facts, and neither
// imports the other's internals. The grammar helpers below mirror the shared
// v1 contract rules as request-boundary guards.
var (
	// ErrAlreadyInstalled marks installing an app whose project already has
	// an active installation pinned to a different version — or to the same
	// version but a different grant, which must never silently re-grant.
	ErrAlreadyInstalled = errors.New("app is already installed with a different version")
	// ErrIdempotencyConflict marks an installation command key that was
	// consumed by a different canonical request.
	ErrIdempotencyConflict = errors.New("installation idempotency key was used for a different request")
	// ErrInvalidGrant marks a syntactically malformed grant snapshot: empty
	// or control-character capability IDs, or duplicates. Transport maps it
	// to a sanitized InvalidArgument.
	ErrInvalidGrant = errors.New("granted permissions are malformed")
	// ErrGrantNotRequested marks a grant that is not a subset of the pinned
	// manifest version's requested permissions. Requesting a capability is
	// never granting it; the verdict is a sanitized PermissionDenied.
	ErrGrantNotRequested = errors.New("granted permission was not requested by the app")
	// ErrGrantNotCompatible marks a version transition whose target
	// requested-permission set does not cover the installation's current
	// grant set (ADR-0012). Permissions are never expanded — not even by an
	// upgrade — and never re-widened by a rollback; the owner must re-run
	// the explicit grant consent first. Sanitized FailedPrecondition.
	ErrGrantNotCompatible = errors.New("current permissions are not compatible with the target version")
	// ErrNoPreviousVersion marks a rollback against an installation whose
	// durable history holds no pinned snapshot different from the current
	// (version, digest). Sanitized FailedPrecondition with zero side
	// effects.
	ErrNoPreviousVersion = errors.New("no previous version to roll back to")
)

// Installation is one durable app instance installed in a project. The ID is
// the stable instance identity future surfaces reference; UninstalledAt being
// nil means the installation is active.
type Installation struct {
	ID             string
	OwnerUserID    string
	ProjectID      string
	AppID          string
	Version        string
	ManifestDigest string
	// GrantedPermissions is the current canonical grant snapshot: canonical
	// sorted, duplicate-free, always a subset of the pinned version's
	// requested permissions. Historically an immutable install-time snapshot;
	// since the mutable-grants flow (ADR-0003) it is the user's last confirmed
	// complete set. Empty means nothing was granted.
	GrantedPermissions []string
	// GrantRevision is the authorization epoch of GrantedPermissions: it
	// starts at 1 when the installation is created and increases by exactly
	// one only when the grant set actually changes. Clients can never submit
	// or predict it.
	GrantRevision int64
	InstalledAt   time.Time
	UninstalledAt *time.Time
}

// PinnedApp is the neutral registry reference an installation pins at command
// time. Only these immutable identity fields are copied from the registry —
// never manifests or credentials. Permissions carries the pinned version's
// requested permission list; it is the subset boundary for grant validation
// and never becomes a grant by itself.
type PinnedApp struct {
	AppID          string
	Version        string
	ManifestDigest string
	Scope          string
	Permissions    []string
}

// InstallableScope reports whether an app scope may pass through the install
// path. System/trusted scopes must fail closed: the public registry policy
// never accepts them, and the install path must not become a bypass.
func InstallableScope(scope string) bool {
	return scope == "user" || scope == "project"
}

const (
	installationAppIDMinLength = 3
	installationAppIDMaxLength = 63
	// MaxInstallationIdempotencyKeyRunes bounds the install/uninstall command
	// idempotency key: valid UTF-8, no C0/C1/NUL control characters, never
	// trimmed.
	MaxInstallationIdempotencyKeyRunes = 128
	digestHexLength                    = 64
)

// ValidInstallationAppID matches the canonical app-ID grammar shared by the
// manifest schema and the registry database constraint.
func ValidInstallationAppID(value string) bool {
	if len(value) < installationAppIDMinLength || len(value) > installationAppIDMaxLength {
		return false
	}
	if value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		c := value[index]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

// ValidInstallationVersion is the request-boundary SemVer syntax guard
// (schema-compatible with non-empty prerelease identifiers); resolving an
// actual registry version is the catalog port's job.
func ValidInstallationVersion(value string) bool {
	release := value
	prerelease := ""
	hasPrerelease := false
	if index := strings.IndexByte(value, '-'); index >= 0 {
		release, prerelease = value[:index], value[index+1:]
		hasPrerelease = true
	}
	parts := strings.Split(release, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if _, err := strconv.ParseUint(part, 10, 64); err != nil || (len(part) > 1 && part[0] == '0') {
			return false
		}
	}
	if !hasPrerelease {
		return true
	}
	if prerelease == "" {
		return false
	}
	for _, identifier := range strings.Split(prerelease, ".") {
		if identifier == "" {
			return false
		}
		alphanumeric := true
		for index := 0; index < len(identifier); index++ {
			c := identifier[index]
			switch {
			case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
			default:
				alphanumeric = false
			}
		}
		if !alphanumeric {
			return false
		}
	}
	return true
}

// ValidInstallationManifestDigest matches the canonical sha256 digest shape.
func ValidInstallationManifestDigest(value string) bool {
	if len(value) != len("sha256:")+digestHexLength || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for index := len("sha256:"); index < len(value); index++ {
		c := value[index]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidInstallationUUID reports whether value is a canonical hyphenated
// UUID, guarding project and installation identifiers at the boundary.
func ValidInstallationUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, c := range []byte(value) {
		switch index {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return false
			}
		}
	}
	return true
}

// ValidInstallationIdempotencyKey enforces the command key grammar at the
// application boundary.
func ValidInstallationIdempotencyKey(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
		count++
		if count > MaxInstallationIdempotencyKeyRunes {
			return false
		}
	}
	return count > 0
}

// InstallationRequestDigest digests the canonical client request of an
// install/uninstall command. It covers exactly the fields the client sent —
// the requested version, never a resolved registry current — so an idempotent
// replay cannot drift when the registry current later changes. Timestamps and
// random identifiers are never mixed in. The JSON encoder orders struct
// fields alphabetically, making the encoding deterministic.
func InstallationRequestDigest(command, projectID, appID, version, installationID string, expectedRevision int64) string {
	canonical := struct {
		Action           string `json:"action"`
		AppID            string `json:"app_id"`
		ExpectedRevision int64  `json:"expected_project_revision"`
		InstallationID   string `json:"installation_id"`
		ProjectID        string `json:"project_id"`
		Version          string `json:"version"`
	}{Action: command, AppID: appID, ExpectedRevision: expectedRevision, InstallationID: installationID, ProjectID: projectID, Version: version}
	// encoding/json cannot fail on these constrained string fields, and struct
	// fields written in alphabetical order keep the encoding deterministic.
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// InstallationRequestDigestWithGrants digests an install command that carries
// an explicit grant snapshot. Empty grants keep the v1 digest above so every
// request minted before grants existed replays identically after the upgrade;
// a non-empty grant switches to the versioned v2 body that pins the canonical
// sorted grant, so same-key/different-grant requests are stable Aborted
// verdicts instead of silent re-grants.
func InstallationRequestDigestWithGrants(command, projectID, appID, version, installationID string, expectedRevision int64, grantedPermissions []string) string {
	if len(grantedPermissions) == 0 {
		return InstallationRequestDigest(command, projectID, appID, version, installationID, expectedRevision)
	}
	// The canonical grant is already sorted by the application layer; sorting
	// again here keeps the digest order independent no matter the caller.
	sorted := make([]string, len(grantedPermissions))
	copy(sorted, grantedPermissions)
	sort.Strings(sorted)
	canonical := struct {
		Action           string   `json:"action"`
		AppID            string   `json:"app_id"`
		ExpectedRevision int64    `json:"expected_project_revision"`
		Granted          []string `json:"granted_permissions"`
		GrantVersion     string   `json:"grant_version"`
		InstallationID   string   `json:"installation_id"`
		ProjectID        string   `json:"project_id"`
		Version          string   `json:"version"`
	}{
		Action: command, AppID: appID, ExpectedRevision: expectedRevision,
		Granted: sorted, GrantVersion: "v2",
		InstallationID: installationID, ProjectID: projectID, Version: version,
	}
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// SetGrantsRequestDigest digests the canonical client request of the
// SetAppGrants full-replacement command (ADR-0003). It covers exactly the
// client-submitted facts — the command version marker, project, installation,
// expected Project revision, and the canonical sorted target grant set — so
// same-key replays compare verbatim while any change of command, project,
// installation, revision, or grant is a different request. Timestamps, random
// identifiers, and server-resolved facts (catalog results, current grant,
// current grant revision) never enter it. The grant list is sorted here so the
// digest is order independent no matter the caller, and empty still means
// "revoke all" — a distinct digest component, never a fallback.
func SetGrantsRequestDigest(projectID, installationID string, expectedRevision int64, grantedPermissions []string) string {
	sorted := make([]string, len(grantedPermissions))
	copy(sorted, grantedPermissions)
	sort.Strings(sorted)
	canonical := struct {
		Command          string   `json:"command"`
		ExpectedRevision int64    `json:"expected_project_revision"`
		Granted          []string `json:"granted_permissions"`
		GrantVersion     string   `json:"grant_version"`
		InstallationID   string   `json:"installation_id"`
		ProjectID        string   `json:"project_id"`
	}{
		Command: "set-grants/v1", ExpectedRevision: expectedRevision,
		Granted: sorted, GrantVersion: "v1",
		InstallationID: installationID, ProjectID: projectID,
	}
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// VersionHistoryLimit bounds the append-only version history of one
// installation (ADR-0012). The application trims snapshots beyond the most
// recent limit inside the command transaction, so rollback depth is a
// documented, finite policy instead of unbounded growth.
const VersionHistoryLimit = 20

// Version history snapshot sources. `install` is the origin every
// installation seeds at creation; `transition` and `rollback` mark the two
// owner-triggered commands.
const (
	VersionSourceInstall    = "install"
	VersionSourceTransition = "transition"
	VersionSourceRollback   = "rollback"
)

// TransitionRequestDigest digests the canonical client request of the
// explicit version transition (ADR-0012): the command version marker,
// project, installation, expected Project revision, and the requested target
// version. Server-resolved facts (the registry's manifest digest for that
// version, the current version) never enter it.
func TransitionRequestDigest(projectID, installationID string, expectedRevision int64, version string) string {
	canonical := struct {
		Command          string `json:"command"`
		ExpectedRevision int64  `json:"expected_project_revision"`
		InstallationID   string `json:"installation_id"`
		ProjectID        string `json:"project_id"`
		Version          string `json:"version"`
	}{
		Command: "transition/v1", ExpectedRevision: expectedRevision,
		InstallationID: installationID, ProjectID: projectID, Version: version,
	}
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// RollbackRequestDigest digests the canonical client request of the
// previous-pinned-version rollback (ADR-0012). The request carries no target:
// Core derives it from the durable history, so the digest covers only the
// command version marker, project, installation, and expected revision.
func RollbackRequestDigest(projectID, installationID string, expectedRevision int64) string {
	canonical := struct {
		Command          string `json:"command"`
		ExpectedRevision int64  `json:"expected_project_revision"`
		InstallationID   string `json:"installation_id"`
		ProjectID        string `json:"project_id"`
	}{
		Command: "rollback/v1", ExpectedRevision: expectedRevision,
		InstallationID: installationID, ProjectID: projectID,
	}
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// VersionSnapshot is one immutable entry of an installation's version
// history: the pinned identity facts as of that change.
type VersionSnapshot struct {
	Version        string
	ManifestDigest string
	Source         string
	Sequence       int64
	OccurredAt     time.Time
}

// GrantsCompatibleWithTarget reports whether the installation's current
// canonical grant set is a subset of the target version's requested
// permissions (ADR-0012). A transition or rollback whose target does not
// cover the current grants would silently change authority, so it is a
// distinct fail-closed verdict: the owner reviews permissions explicitly
// (SetAppGrants) before moving versions. The grant set itself is never
// modified by a version change.
func GrantsCompatibleWithTarget(granted, requested []string) error {
	if _, err := CanonicalInstallationGrant(granted, requested); err != nil {
		return ErrGrantNotCompatible
	}
	return nil
}

// ErrHistoryCorrupt marks stored version-history snapshots that violate the
// canonical invariants. Callers surface it as sanitized Internal corruption,
// never as a silent repair.
var ErrHistoryCorrupt = errors.New("stored installation version history is inconsistent")

// ValidateVersionHistory re-validates stored snapshots on every read:
// grammar-valid version, canonical digest shape, known source, positive
// strictly-increasing sequences. Drift is corruption, never a repair hint.
func ValidateVersionHistory(history []VersionSnapshot) error {
	previous := int64(0)
	for _, snapshot := range history {
		if !ValidInstallationVersion(snapshot.Version) ||
			!ValidInstallationManifestDigest(snapshot.ManifestDigest) ||
			(snapshot.Source != VersionSourceInstall &&
				snapshot.Source != VersionSourceTransition &&
				snapshot.Source != VersionSourceRollback) ||
			snapshot.Sequence <= previous {
			return ErrHistoryCorrupt
		}
		previous = snapshot.Sequence
	}
	return nil
}

// CanonicalGrantShape validates the request-boundary grammar of a
// client-submitted grant snapshot: capability IDs must be non-empty,
// well-formed, and duplicate-free (ErrInvalidGrant). The returned grant is
// canonically sorted; the subset rule against the pinned version's requested
// permissions is a separate step (CanonicalInstallationGrant) that needs the
// catalog result and therefore runs after resolution.
func CanonicalGrantShape(granted []string) ([]string, error) {
	if len(granted) == 0 {
		return []string{}, nil
	}
	seen := make(map[string]struct{}, len(granted))
	canonical := make([]string, 0, len(granted))
	for _, id := range granted {
		if !ValidCapabilityID(id) {
			return nil, ErrInvalidGrant
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, ErrInvalidGrant
		}
		seen[id] = struct{}{}
		canonical = append(canonical, id)
	}
	sort.Strings(canonical)
	return canonical, nil
}

// CanonicalInstallationGrant re-checks an already canonicalized grant against
// the pinned version's requested permissions: the whole set must be a subset
// (ErrGrantNotRequested). Requesting a capability is never granting it.
func CanonicalInstallationGrant(granted, requested []string) ([]string, error) {
	if len(granted) == 0 {
		return []string{}, nil
	}
	requestedSet := make(map[string]struct{}, len(requested))
	for _, id := range requested {
		requestedSet[id] = struct{}{}
	}
	for _, id := range granted {
		if _, known := requestedSet[id]; !known {
			return nil, ErrGrantNotRequested
		}
	}
	return granted, nil
}

// ValidCapabilityID is the manifest-schema capability shape
// (^[a-z][a-z0-9.-]+$). Vocabulary membership itself is the registry's
// concern: a well-formed capability that the pinned manifest never requested
// is rejected by the subset rule, so this module never needs its own copy of
// the vocabulary.
func ValidCapabilityID(value string) bool {
	if len(value) < 2 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 0; index < len(value); index++ {
		c := value[index]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '-' {
			continue
		}
		return false
	}
	return true
}
