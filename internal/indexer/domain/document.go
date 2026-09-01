// Indexer projection domain facts (ADR-0013): canonical documents, resolved
// sources, and the durable consumption verdicts. The projection is a
// rebuildable copy — every fact is revalidated on read and drift is
// corruption, never a silent repair.
package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalid     = errors.New("indexer projection fact is invalid")
	ErrCorrupt     = errors.New("stored index projection failed validation")
	ErrNotFound    = errors.New("indexer projection fact is not available")
	ErrUnavailable = errors.New("indexer projection store is temporarily unavailable")
)

// SourceType is the only indexed source family.
const SourceType = "artifact.review.v1"

// Document is one immutable projected review artifact.
type Document struct {
	OwnerUserID     string
	ProjectID       string
	SourceID        string
	SourceDigest    string
	ArtifactType    string
	Title           string
	Content         string
	SourceCreatedAt time.Time
	LastPublication string
	IndexedAt       time.Time
}

// ValidUUID reports whether the value is a canonical lowercase UUIDv7 string.
func ValidUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return false
	}
	return parsed.Version() == 7 && parsed.Variant() == uuid.RFC4122
}

// ValidDigest checks the canonical `sha256:<64 lowercase hex>` grammar.
func ValidDigest(digest string) bool {
	const prefix = "sha256:"
	if len(digest) != len(prefix)+64 || !strings_HasPrefix(digest, prefix) {
		return false
	}
	for _, r := range digest[len(prefix):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func strings_HasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}

// CanonicalUTCTime truncates to database microsecond precision.
func CanonicalUTCTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

// ValidStoredDocument revalidates one stored document row on every read.
func ValidStoredDocument(document Document) error {
	if !ValidUUID(document.OwnerUserID) || !ValidUUID(document.ProjectID) || !ValidUUID(document.SourceID) ||
		!ValidUUID(document.LastPublication) {
		return ErrCorrupt
	}
	if !ValidDigest(document.SourceDigest) {
		return ErrCorrupt
	}
	switch document.ArtifactType {
	case "document.markdown.v1", "code.unified-diff.v1":
	default:
		return ErrCorrupt
	}
	if len(document.Title) < 1 || len([]rune(document.Title)) > 200 {
		return ErrCorrupt
	}
	if len(document.Content) == 0 || len(document.Content) > 512*1024 {
		return ErrCorrupt
	}
	if document.SourceCreatedAt.IsZero() || document.IndexedAt.IsZero() {
		return ErrCorrupt
	}
	return nil
}

// ValidStoredScore pins the response score grammar: finite, non-negative,
// inside the documented fixed range.
func ValidStoredScore(score float64) error {
	if score != score || score > ScoreBoundUpper || score < 0 || fmt.Sprintf("%g", score) == "+Inf" {
		return ErrCorrupt
	}
	return nil
}

// Consumption verdicts recorded per (publication, generation).
const (
	OutcomeApplied     = "applied"
	OutcomeTombstoned  = "tombstoned"
	OutcomeUnsupported = "unsupported"
	OutcomeCorrupt     = "corrupt"
)

// ValidWorkerID checks the bounded internal worker identity grammar.
func ValidWorkerID(workerID string) bool {
	if len(workerID) == 0 || len(workerID) > 128 {
		return false
	}
	for _, r := range workerID {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '.' || r == '_':
		default:
			return false
		}
	}
	return true
}
