// Index publication facts (ADR-0013): the Core-side durable authority that a
// review artifact became index-source or a project was archived. A
// publication never carries artifact content, task goals, provider output,
// credentials, or user display names — only stable identity facts the
// indexer needs to claim, resolve, and acknowledge consumption.
package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Publication operations. The exact strings are part of the stored contract
// (migration 026 CHECK) and the wire contract (additive proto enum).
const (
	OperationReviewArtifactUpsert = "review-artifact.upsert"
	OperationProjectTombstone     = "project.tombstone"
)

// SourceType is the only implemented source family. Future source types need
// a new ADR and an additive enum value; they can never reuse these facts.
const SourceType = "artifact.review.v1"

// Publication outcomes. completed/tombstoned are successful consumption;
// unsupported/corrupt are observable degraded terminal outcomes. Transient
// outages are never recorded — they stay retryable claims.
const (
	OutcomeCompleted   = "completed"
	OutcomeTombstoned  = "tombstoned"
	OutcomeUnsupported = "unsupported"
	OutcomeCorrupt     = "corrupt"
)

var (
	ErrInvalid = errors.New("index publication fact is invalid")
	// ErrCorrupt marks stored publication drift: immutable facts cannot
	// change, so any violation is internal corruption answered with a
	// sanitized failure, never a silent repair.
	ErrCorrupt = errors.New("stored index publication failed validation")
	// ErrLeaseStale marks a complete/ack whose claim is no longer the live
	// lease (expired, re-claimed, or already completed). The consumer must
	// safely replay; the local receipt makes the replay a no-op.
	ErrLeaseStale = errors.New("index publication lease is not live")
	// ErrNotFound marks an unknown publication id. Like every lookup miss it
	// is a sanitized failure without existence detail.
	ErrNotFound = errors.New("index publication is not available")
)

// Publication is one durable index feed fact. Upsert facts carry the exact
// source identity and digest; tombstone facts carry only the project scope.
type Publication struct {
	ID           string
	Operation    string
	OwnerUserID  string
	ProjectID    string
	SourceType   string
	SourceID     string
	ArtifactType string
	Digest       string
	OccurredAt   time.Time
}

// ValidUUID reports whether the value is a canonical lowercase UUIDv7 string.
func ValidUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return false
	}
	return parsed.Version() == 7 && parsed.Variant() == uuid.RFC4122
}

// CanonicalUTCTime truncates to database microsecond precision so stored
// facts replay byte-exactly after a restart.
func CanonicalUTCTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

// ValidStoredPublication revalidates a stored publication row on every read.
// Drift of any immutable fact is corruption, never a repairable state.
func ValidStoredPublication(publication Publication) error {
	if !ValidUUID(publication.ID) || !ValidUUID(publication.OwnerUserID) || !ValidUUID(publication.ProjectID) {
		return ErrCorrupt
	}
	if publication.SourceType != SourceType {
		return ErrCorrupt
	}
	if publication.OccurredAt.IsZero() {
		return ErrCorrupt
	}
	switch publication.Operation {
	case OperationReviewArtifactUpsert:
		if !ValidUUID(publication.SourceID) || publication.ArtifactType == "" || !validDigest(publication.Digest) {
			return ErrCorrupt
		}
	case OperationProjectTombstone:
		if publication.SourceID != "" || publication.ArtifactType != "" || publication.Digest != "" {
			return ErrCorrupt
		}
	default:
		return ErrCorrupt
	}
	return nil
}

func validDigest(digest string) bool {
	if len(digest) != len("sha256:")*0+71 { // "sha256:" (7) + 64 hex
		return false
	}
	if digest[:7] != "sha256:" {
		return false
	}
	for _, r := range digest[7:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// ValidDigest exposes the canonical digest grammar for callers preparing
// publication facts.
func ValidDigest(digest string) bool { return validDigest(digest) }

// ValidWorkerID checks the bounded internal worker identity grammar
// (1..128 ASCII [-a-z0-9._]). It is persisted as the lease owner and is
// never accepted from browsers or apps.
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

// Claim lease bounds. Short enough to recover quickly from a crashed worker,
// long enough that a slow resolve is not stolen mid-flight.
const (
	MinLeaseSeconds = 5
	MaxLeaseSeconds = 300
	MaxClaimBatch   = 16
)

func ClampLeaseSeconds(seconds int32) time.Duration {
	if seconds < MinLeaseSeconds {
		seconds = MinLeaseSeconds
	}
	if seconds > MaxLeaseSeconds {
		seconds = MaxLeaseSeconds
	}
	return time.Duration(seconds) * time.Second
}

// OutcomeFor classifies a resolve verdict + operation into the durable
// outcome the consumer is allowed to record. Any other combination is a
// programming error and fails closed.
func OutcomeFor(operation, verdict string) (string, error) {
	switch verdict {
	case "resolved":
		if operation == OperationReviewArtifactUpsert {
			return OutcomeCompleted, nil
		}
	case "tombstoned":
		return OutcomeTombstoned, nil
	case "corrupt":
		return OutcomeCorrupt, nil
	case "unsupported":
		return OutcomeUnsupported, nil
	}
	return "", fmt.Errorf("%w: verdict %q for operation %q", ErrInvalid, verdict, operation)
}
