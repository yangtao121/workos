package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid  = errors.New("invalid project")
	ErrNotFound = errors.New("project not found")
	ErrConflict = errors.New("project revision conflict")
	// ErrIdempotencyConflict (declared in installation.go) marks a command
	// key consumed by a different canonical request — for the base create
	// path it also covers legacy keys whose original request is unknown and
	// therefore can never be adjudicated.
)

type WorkspaceRef struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	URI          string `json:"uri"`
	LogicalMount string `json:"logicalMount"`
	ReadOnly     bool   `json:"readOnly"`
}

type HarnessBinding struct {
	ProviderID       string `json:"providerId"`
	InstancePolicy   string `json:"instancePolicy"`
	ProfileID        string `json:"profileId,omitempty"`
	CredentialRef    string `json:"credentialRef,omitempty"`
	ResourcePolicyID string `json:"resourcePolicyId"`
}

type Project struct {
	ID                    string
	OwnerUserID           string
	Name                  string
	Icon                  string
	WorkspaceRefs         []WorkspaceRef
	HarnessBinding        *HarnessBinding
	InstalledAppIDs       []string
	DefaultAgentRole      string
	KnowledgeCollectionID string
	ArtifactCollectionID  string
	Revision              int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	ArchivedAt            *time.Time
}

// Request-boundary limits. Every client-supplied text field has an explicit
// maximum; the public wire budget (transport MaxRequestBytes) is derived from
// these numbers, so changing one requires revisiting the other.
const (
	// MaxNameRunes keeps the existing trim-then-1..120 rule (also the
	// database CHECK in migration 001).
	MaxNameRunes = 120
	// MaxIconRunes bounds the optional display icon.
	MaxIconRunes = 128
	// MaxWorkspaceRefs bounds how many workspace references a project pins.
	MaxWorkspaceRefs = 16
	// MaxWorkspaceRefIDRunes bounds each reference's client identifier.
	MaxWorkspaceRefIDRunes = 128
	// MaxWorkspaceRefURIRunes bounds each reference URI.
	MaxWorkspaceRefURIRunes = 1024
	// MaxLogicalMountRunes bounds the optional logical mount label.
	MaxLogicalMountRunes = 128
	// MaxProviderIDRunes / MaxPolicyReferenceRunes / MaxCredentialRefRunes
	// bound the harness binding's references. credential_ref stays a bounded
	// opaque reference — never a raw credential.
	MaxProviderIDRunes      = 128
	MaxPolicyReferenceRunes = 128
	MaxCredentialRefRunes   = 256
	// MaxIdempotencyKeyRunes bounds the create command key: valid UTF-8, no
	// C0/C1 control characters, never trimmed.
	MaxIdempotencyKeyRunes = 128
)

// workspaceKinds is the closed set of declared WorkspaceKind values a
// workspace reference may carry. WORKSPACE_KIND_UNSPECIFIED and undeclared
// numeric enum values never reach storage.
var workspaceKinds = map[string]struct{}{
	"WORKSPACE_KIND_LOCAL_GIT":        {},
	"WORKSPACE_KIND_LOCAL_DIRECTORY":  {},
	"WORKSPACE_KIND_NAS":              {},
	"WORKSPACE_KIND_REMOTE_DIRECTORY": {},
	"WORKSPACE_KIND_DATASET":          {},
	"WORKSPACE_KIND_ARTIFACTS":        {},
}

func NormalizeName(value string) (string, error) {
	value = strings.TrimSpace(value)
	// The trimmed name follows the same text grammar as every other
	// client-supplied field: valid UTF-8, no C0/C1 control characters, so an
	// interior newline or escape survives trimming but never reaches storage.
	if !requiredText(value, MaxNameRunes) {
		return "", ErrInvalid
	}
	return value, nil
}

// ValidateIcon checks the optional icon: valid UTF-8, at most MaxIconRunes
// code points, no C0/C1 control characters. The value is stored verbatim —
// icons are never trimmed.
func ValidateIcon(value string) error {
	if !validText(value, MaxIconRunes) {
		return ErrInvalid
	}
	return nil
}

// ValidateWorkspaceRefs checks the whole reference list: bounded count,
// well-formed per-reference fields, unique reference IDs, and unambiguous
// (unique) non-empty logical mounts. The submitted order is preserved — it is
// part of the create request digest. A nil/empty list is legal.
func ValidateWorkspaceRefs(refs []WorkspaceRef) error {
	if len(refs) > MaxWorkspaceRefs {
		return ErrInvalid
	}
	ids := make(map[string]struct{}, len(refs))
	mounts := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if !requiredText(ref.ID, MaxWorkspaceRefIDRunes) {
			return ErrInvalid
		}
		if _, duplicate := ids[ref.ID]; duplicate {
			return ErrInvalid
		}
		ids[ref.ID] = struct{}{}
		if _, known := workspaceKinds[ref.Kind]; !known {
			// UNSPECIFIED and unknown enum values both land here.
			return ErrInvalid
		}
		if !requiredText(ref.URI, MaxWorkspaceRefURIRunes) {
			return ErrInvalid
		}
		if !validText(ref.LogicalMount, MaxLogicalMountRunes) {
			return ErrInvalid
		}
		if ref.LogicalMount != "" {
			if _, ambiguous := mounts[ref.LogicalMount]; ambiguous {
				return ErrInvalid
			}
			mounts[ref.LogicalMount] = struct{}{}
		}
	}
	return nil
}

// ValidateBinding checks an optional harness binding: references are bounded,
// the instance policy is a declared value, and a raw credential is never a
// legal input (credential_ref is an opaque bounded reference). A nil binding
// is legal and means "no binding".
func ValidateBinding(binding *HarnessBinding) error {
	if binding == nil {
		return nil
	}
	if !requiredText(binding.ProviderID, MaxProviderIDRunes) ||
		!requiredText(binding.ResourcePolicyID, MaxPolicyReferenceRunes) {
		return ErrInvalid
	}
	switch binding.InstancePolicy {
	case "persistent", "lazy", "ephemeral":
	default:
		return ErrInvalid
	}
	if !validText(binding.ProfileID, MaxPolicyReferenceRunes) ||
		!validText(binding.CredentialRef, MaxCredentialRefRunes) {
		return ErrInvalid
	}
	return nil
}

// ValidProjectUUID reports whether value is the canonical project/collection
// identifier shape the server mints: a lowercase hyphenated UUIDv7
// (version nibble 7, RFC 4122 variant). It is the one neutral validator for
// Project IDs and list cursors — case is significant, uppercase input is a
// malformed identifier, not an alias.
func ValidProjectUUID(value string) bool {
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
			if c < '0' || c > '9' {
				if c < 'a' || c > 'f' {
					return false
				}
			}
		}
	}
	if value[14] != '7' {
		return false
	}
	switch value[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
}

// ValidIdempotencyKey enforces the create command key grammar at the
// application boundary: valid UTF-8, 1..MaxIdempotencyKeyRunes Unicode code
// points, no C0/C1 control characters.
func ValidIdempotencyKey(value string) bool {
	return value != "" && validText(value, MaxIdempotencyKeyRunes)
}

// validText reports whether value is valid UTF-8, at most maxRunes Unicode
// code points, and free of C0/C1 control characters (which includes NUL and
// DEL). Empty is legal here; callers that require content check that first.
func validText(value string, maxRunes int) bool {
	if !utf8.ValidString(value) {
		return false
	}
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
		count++
		if count > maxRunes {
			return false
		}
	}
	return count <= maxRunes
}

// requiredText is validText for fields that must carry content.
func requiredText(value string, maxRunes int) bool {
	return value != "" && validText(value, maxRunes)
}

type canonicalWorkspaceRef struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	URI          string `json:"uri"`
	LogicalMount string `json:"logical_mount"`
	ReadOnly     bool   `json:"read_only"`
}

type canonicalHarnessBinding struct {
	CredentialRef    string `json:"credential_ref"`
	InstancePolicy   string `json:"instance_policy"`
	ProfileID        string `json:"profile_id"`
	ProviderID       string `json:"provider_id"`
	ResourcePolicyID string `json:"resource_policy_id"`
}

type canonicalCreateRequest struct {
	Command        string                   `json:"command"`
	Icon           string                   `json:"icon"`
	Name           string                   `json:"name"`
	WorkspaceRefs  []canonicalWorkspaceRef  `json:"workspace_refs"`
	HarnessBinding *canonicalHarnessBinding `json:"harness_binding"`
}

// createCommandVersion marks the canonical create request encoding. A future
// incompatible change to the covered fields must bump it so old records stay
// comparable against the encoding that produced them.
const createCommandVersion = "project.create/v1"

// CreateRequestDigest digests the canonical client request of one create
// command (ADR-0004). It covers exactly the normalized client inputs — the
// trimmed name, verbatim icon, the submitted-order workspace references with
// every public field, and the optional harness binding whose presence is
// itself semantic. It never mixes in the owner (owner + key are the
// namespace, not the request), server-generated identifiers, timestamps,
// revisions, or any database state. encoding/json orders struct fields
// alphabetically and cannot fail on these constrained strings, so the
// encoding is deterministic; the sha256 hex form matches the digest column
// CHECK in migration 013.
func CreateRequestDigest(name, icon string, refs []WorkspaceRef, binding *HarnessBinding) string {
	canonical := canonicalCreateRequest{
		Command: createCommandVersion, Icon: icon, Name: name,
		WorkspaceRefs: make([]canonicalWorkspaceRef, 0, len(refs)),
	}
	for _, ref := range refs {
		canonical.WorkspaceRefs = append(canonical.WorkspaceRefs, canonicalWorkspaceRef{
			ID: ref.ID, Kind: ref.Kind, URI: ref.URI, LogicalMount: ref.LogicalMount, ReadOnly: ref.ReadOnly,
		})
	}
	if binding != nil {
		canonical.HarnessBinding = &canonicalHarnessBinding{
			CredentialRef: binding.CredentialRef, InstancePolicy: binding.InstancePolicy,
			ProfileID: binding.ProfileID, ProviderID: binding.ProviderID,
			ResourcePolicyID: binding.ResourcePolicyID,
		}
	}
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
