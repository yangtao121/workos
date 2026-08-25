package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	// an active installation pinned to a different version.
	ErrAlreadyInstalled = errors.New("app is already installed with a different version")
	// ErrIdempotencyConflict marks an installation command key that was
	// consumed by a different canonical request.
	ErrIdempotencyConflict = errors.New("installation idempotency key was used for a different request")
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
	InstalledAt    time.Time
	UninstalledAt  *time.Time
}

// PinnedApp is the neutral registry reference an installation pins at command
// time. Only these immutable identity fields are copied from the registry —
// never manifests or credentials.
type PinnedApp struct {
	AppID          string
	Version        string
	ManifestDigest string
	Scope          string
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
