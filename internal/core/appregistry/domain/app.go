package domain

import (
	"errors"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid             = errors.New("invalid app registration")
	ErrNotFound            = errors.New("app not found")
	ErrVersionExists       = errors.New("app version already exists with a different manifest")
	ErrIdempotencyConflict = errors.New("idempotency key was used for a different request")
)

// AppVersion is one immutable registered manifest version owned by one owner:
// (OwnerUserID, AppID, Version) resolves to exactly one ManifestDigest.
type AppVersion struct {
	ID                string
	OwnerUserID       string
	AppID             string
	Version           string
	Scope             Scope
	Name              string
	Permissions       []string
	ManifestDigest    string
	CanonicalManifest []byte
	IdempotencyKey    string
	RequestDigest     string
	CreatedAt         time.Time
}

// AppVersionSummary is the bounded read projection for public queries: it
// never carries the canonical manifest, so summaries cannot materialize
// manifest bytes regardless of how many versions an app has.
type AppVersionSummary struct {
	AppID          string
	Version        string
	Scope          Scope
	Name           string
	Permissions    []string
	ManifestDigest string
}

// SummaryOf projects a stored or freshly written version record.
func SummaryOf(record AppVersion) AppVersionSummary {
	return AppVersionSummary{
		AppID: record.AppID, Version: record.Version, Scope: record.Scope,
		Name: record.Name, Permissions: record.Permissions, ManifestDigest: record.ManifestDigest,
	}
}

// AppIDGrammar is the canonical app identifier grammar shared by the manifest
// schema and the database check constraint.
const (
	appIDMinLength = 3
	appIDMaxLength = 63
)

// ValidAppID reports whether value matches the canonical app-ID grammar
// ^[a-z][a-z0-9-]{2,62}$ (ASCII-only, so byte-wise checks are exact).
func ValidAppID(value string) bool {
	if len(value) < appIDMinLength || len(value) > appIDMaxLength {
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

// ValidUUID reports whether value is a canonical hyphenated UUID. It is a
// request-boundary guard so malformed identifiers fail as InvalidArgument
// instead of reaching the database driver.
func ValidUUID(value string) bool {
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

// MaxIdempotencyKeyRunes bounds the registration idempotency key. Keys must be
// valid UTF-8 without C0/C1/NUL control characters; they are never trimmed.
const MaxIdempotencyKeyRunes = 128

// ValidIdempotencyKey enforces the key grammar at the application boundary.
func ValidIdempotencyKey(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
		count++
		if count > MaxIdempotencyKeyRunes {
			return false
		}
	}
	return count > 0
}

// CurrentVersion selects the highest version by SemVer precedence.
func CurrentVersion(versions []AppVersion) (AppVersion, bool) {
	if len(versions) == 0 {
		return AppVersion{}, false
	}
	current := versions[0]
	currentParsed, ok := ParseVersion(current.Version)
	if !ok {
		return AppVersion{}, false
	}
	for _, candidate := range versions[1:] {
		candidateParsed, ok := ParseVersion(candidate.Version)
		if !ok {
			return AppVersion{}, false
		}
		if CompareVersion(candidateParsed, currentParsed) > 0 {
			current, currentParsed = candidate, candidateParsed
		}
	}
	return current, true
}
