package domain

import (
	"errors"
	"time"
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
