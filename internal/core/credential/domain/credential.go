// Package domain holds the Credential Vault's pure facts: the metadata
// projection every external surface sees, the grammar every request must
// satisfy, and the lease verdict types. It never imports storage, wire, or
// crypto packages; encryption lives behind the ports.Cipher port.
package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrInvalid covers every malformed bounded input with one sanitized
	// verdict; transports map it to InvalidArgument.
	ErrInvalid = errors.New("invalid credential request")
	// ErrNotFound is returned for unknown or foreign credentials and never
	// distinguishes the two.
	ErrNotFound = errors.New("credential not found")
	// ErrConflict marks a lost expected-revision race.
	ErrConflict = errors.New("credential revision conflict")
	// ErrIdempotencyConflict marks a consumed admin idempotency key reused
	// for a different canonical request.
	ErrIdempotencyConflict = errors.New("credential idempotency key was used for a different request")
	// ErrAlreadyExists marks a Put when an active credential already exists
	// for the same (owner, consumer, purpose).
	ErrAlreadyExists = errors.New("an active credential already exists for this consumer and purpose")
	// ErrCorrupt marks stored ciphertext, metadata, or snapshot facts that
	// fail their invariants. It is sanitized Internal and never repaired.
	ErrCorrupt = errors.New("stored credential data is corrupt")
	// ErrUnavailable marks transient storage or vault unavailability.
	ErrUnavailable = errors.New("credential vault is temporarily unavailable")
	// ErrLeaseLost marks an acquire/renew against a lost, expired, released,
	// or foreign task or credential lease.
	ErrLeaseLost = errors.New("task credential lease is not active")
	// ErrRevoked marks a reveal attempted against a revoked credential.
	// Revoked material never decrypts for any surface (ADR-0015).
	ErrRevoked = errors.New("credential is revoked")
)

// Credential status facts.
const (
	StatusActive  = "active"
	StatusRevoked = "revoked"

	LeaseStatusActive   = "active"
	LeaseStatusReleased = "released"
	LeaseStatusExpired  = "expired"
)

// Canonical credential kinds (purposes). The vocabulary is finite: a new
// kind requires a domain grammar here, an audit-visible purpose, and a
// migration CHECK update (ADR-0015). Provider-specific protocol details stay
// in the owning harness adapters; the vault only knows these bounded kinds.
const (
	PurposeProviderAPIKeyV1 = "provider-api-key.v1"
	PurposeCodexAuthV1      = "codex-auth.v1"
	PurposeGitHubTokenV1    = "github-token.v1"
	PurposeCloudCredential  = "cloud-credential.v1"
)

// cloudCredentialMaxKeys bounds the flat JSON object grammar of the generic
// cloud credential kind. Values are strings only: structured nesting would
// smuggle unbounded material past the secret bounds.
const cloudCredentialMaxKeys = 32

// Secret material bounds. Secrets are bytes: never trimmed, never normalized,
// never logged. NUL, CR, and LF are rejected at the boundary because no
// known provider API key contains them and they are indistinguishable from
// truncation or transport damage; adapter-specific grammars stay in the
// owning adapters.
const (
	MinSecretBytes = 1
	MaxSecretBytes = 8192
)

// Credential is the metadata projection: zero secret material by
// construction. ID is the server-minted UUIDv7 and the sole opaque
// reference bound into Project bindings and task snapshots.
type Credential struct {
	ID          string
	OwnerUserID string
	ConsumerID  string
	Purpose     string
	Label       string
	Revision    int64
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SealedMaterial is one stored ciphertext pair plus the revision it belongs
// to. It exists only inside the Core process boundary.
type SealedMaterial struct {
	Nonce      []byte
	Ciphertext []byte
}

// ValidConsumerID enforces the canonical consumer grammar: 1..128
// ASCII lowercase letters, digits, dot, underscore, hyphen, after trimming.
func ValidConsumerID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, character := range []byte(value) {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= '0' && character <= '9':
		case character == '.' || character == '_' || character == '-':
		default:
			return false
		}
	}
	return true
}

// ValidPurpose accepts only explicitly supported canonical purposes.
func ValidPurpose(value string) bool {
	switch value {
	case PurposeProviderAPIKeyV1, PurposeCodexAuthV1, PurposeGitHubTokenV1, PurposeCloudCredential:
		return true
	default:
		return false
	}
}

// ValidLabel enforces the optional bounded human label: valid UTF-8, at most
// 80 code points, no C0/C1 control characters.
func ValidLabel(value string) bool {
	if value == "" {
		return true
	}
	if !utf8.ValidString(value) {
		return false
	}
	count := 0
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
		count++
		if count > 80 {
			return false
		}
	}
	return true
}

// ValidSecretBytes enforces the shared boundary grammar for every kind:
// bounded material without NUL, CR, or LF.
func ValidSecretBytes(secret []byte) bool {
	if len(secret) < MinSecretBytes || len(secret) > MaxSecretBytes {
		return false
	}
	for _, b := range secret {
		if b == 0 || b == '\r' || b == '\n' {
			return false
		}
	}
	return true
}

// ValidSecret enforces the per-kind boundary grammar for raw credential
// material (ADR-0015). The shared byte rules come first for every kind; the
// kind-specific shapes then decide acceptance. GitHub tokens must be visible
// ASCII without whitespace; cloud credentials must be a flat JSON object of
// string values; every other kind only needs the bounded byte rules.
func ValidSecret(purpose string, secret []byte) bool {
	if !ValidPurpose(purpose) || !ValidSecretBytes(secret) {
		return false
	}
	switch purpose {
	case PurposeGitHubTokenV1:
		if len(secret) < 20 || len(secret) > 255 {
			return false
		}
		for _, b := range secret {
			if b <= 0x20 || b >= 0x7f {
				return false
			}
		}
		return true
	case PurposeCloudCredential:
		return validCloudCredentialObject(secret)
	default:
		return true
	}
}

// validCloudCredentialObject accepts exactly one flat JSON object of at most
// cloudCredentialMaxKeys string-valued fields within the shared byte bounds.
// Invalid UTF-8, nested values, non-string values, and oversized values fail
// closed: the vault validates the declared kind, it does not parse provider
// protocols.
func validCloudCredentialObject(secret []byte) bool {
	if !utf8.Valid(secret) {
		return false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(secret, &object); err != nil {
		return false
	}
	if len(object) == 0 || len(object) > cloudCredentialMaxKeys {
		return false
	}
	for key, raw := range object {
		if key == "" || len(key) > 128 {
			return false
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return false
		}
		if len(value) == 0 || len(value) > 4096 || strings.ContainsRune(value, 0) {
			return false
		}
	}
	return true
}

// ValidIdempotencyKey enforces the admin write key grammar: 1..128 bytes of
// bounded UTF-8 without control characters.
func ValidIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// ValidWorkerID enforces the stored execution-worker identifier grammar.
func ValidWorkerID(value string) bool {
	if value == "" || len(value) > 128 || value != strings.TrimSpace(value) || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// ValidCredentialID reports whether value is a canonical lowercase UUIDv7.
func ValidCredentialID(value string) bool {
	return validUUIDv7(value)
}

// ValidRevision reports whether revision is a positive revision counter.
func ValidRevision(revision int64) bool {
	return revision >= 1
}

// ValidCredential validates a metadata fact read from durable storage or an
// idempotency snapshot. Invalid stored facts fail closed as ErrCorrupt at the
// adapter/application boundary instead of becoming an external projection.
func ValidCredential(credential Credential) bool {
	if !ValidCredentialID(credential.ID) || !ValidCredentialID(credential.OwnerUserID) ||
		!ValidConsumerID(credential.ConsumerID) || !ValidPurpose(credential.Purpose) ||
		!ValidLabel(credential.Label) || !ValidRevision(credential.Revision) {
		return false
	}
	if credential.Status != StatusActive && credential.Status != StatusRevoked {
		return false
	}
	return ValidStoredUTCTime(credential.CreatedAt) && ValidStoredUTCTime(credential.UpdatedAt) &&
		!credential.UpdatedAt.Before(credential.CreatedAt)
}

// ValidStoredUTCTime accepts only finite UTC timestamps at PostgreSQL's
// microsecond precision. Canonicalizing before writes keeps first responses,
// durable rows, and idempotency replays byte-for-byte consistent.
func ValidStoredUTCTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	year, offset := value.Year(), 0
	_, offset = value.Zone()
	return offset == 0 && year >= 1 && year <= 9999 && value.Equal(CanonicalUTCTime(value))
}

// CanonicalUTCTime matches PostgreSQL timestamptz's microsecond precision.
func CanonicalUTCTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func validUUIDv7(value string) bool {
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
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	// UUIDv7 version nibble: the first hex digit of the third group is 7.
	if value[14] != '7' {
		return false
	}
	// RFC 9562 variant: the first hex digit of the fourth group is 8-b.
	if value[19] != '8' && value[19] != '9' && value[19] != 'a' && value[19] != 'b' {
		return false
	}
	return true
}
