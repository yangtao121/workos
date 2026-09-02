// Reliability-side incident notification publication facts (ADR-0014). A
// publication is appended in the same transaction as its incident and is
// consumed by the Core notification authority over the private source
// service with at-least-once leases. Publications never carry raw
// observations, telemetry, engine output, or content: only identity, scope,
// finite severity/outcome categories, and the versioned digest.
package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// IncidentNotificationPublication is one durable publication fact.
type IncidentNotificationPublication struct {
	ID            string
	IncidentID    string
	OwnerUserID   string
	ProjectID     string
	Severity      string // info | warning | critical
	ActionOutcome string // pending | restarted | stopped | failed
	Digest        string
	OccurredAt    time.Time
}

var (
	ErrPublicationInvalid = errors.New("incident notification publication is invalid")
	// ErrPublicationCorrupt marks stored drift on read or replay.
	ErrPublicationCorrupt = errors.New("stored incident notification publication failed validation")
	// ErrPublicationLeaseStale marks a complete whose claim is no longer
	// live; the Core receipt makes the replay a no-op.
	ErrPublicationLeaseStale = errors.New("incident notification publication lease is not live")
)

// Lease bounds mirror the index feed: short enough to recover quickly from a
// crashed consumer, long enough that a slow apply is not stolen mid-flight.
const (
	PublicationMinLeaseSeconds = 5
	PublicationMaxLeaseSeconds = 300
	PublicationMaxClaimBatch   = 16
)

// ClaimSource leases pending publications (database-arbitrated).
type ClaimSource interface {
	ClaimPendingIncidentPublications(ctx context.Context, workerID, claimToken string, leaseUntil, now time.Time, maxBatch int) ([]IncidentNotificationPublication, error)
	CompleteIncidentPublications(ctx context.Context, workerID, claimToken string, ids []string, now time.Time) (int64, error)
	CountPendingIncidentPublications(ctx context.Context) (int64, error)
}

// ValidWorkerID checks the bounded internal consumer identity grammar
// (1..128 ASCII [-a-z0-9._]).
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

// ClampPublicationLeaseSeconds bounds the requested lease.
func ClampPublicationLeaseSeconds(seconds int32) time.Duration {
	if seconds < PublicationMinLeaseSeconds {
		seconds = PublicationMinLeaseSeconds
	}
	if seconds > PublicationMaxLeaseSeconds {
		seconds = PublicationMaxLeaseSeconds
	}
	return time.Duration(seconds) * time.Second
}

// IncidentNotificationDigest derives the versioned canonical digest of the
// finite publication fields. Core treats a same-source/different-digest
// replay as contract violation, never as an update.
func IncidentNotificationDigest(incidentID, severity, actionOutcome string, occurredAt time.Time) string {
	canonical := fmt.Sprintf("workos.incident-notification.v1|%s|%s|%s|%d",
		incidentID, severity, actionOutcome, occurredAt.UTC().UnixMicro())
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ValidStoredPublication revalidates a stored publication row.
func ValidStoredPublication(publication IncidentNotificationPublication) error {
	if !ValidUUIDv7(publication.ID) || !ValidUUIDv7(publication.IncidentID) ||
		!ValidUUIDv7(publication.OwnerUserID) || !ValidUUIDv7(publication.ProjectID) {
		return ErrPublicationCorrupt
	}
	switch publication.Severity {
	case "info", "warning", "critical":
	default:
		return ErrPublicationCorrupt
	}
	switch publication.ActionOutcome {
	case "pending", "restarted", "stopped", "failed":
	default:
		return ErrPublicationCorrupt
	}
	if len(publication.Digest) != 71 || publication.Digest[:7] != "sha256:" {
		return ErrPublicationCorrupt
	}
	if publication.OccurredAt.IsZero() {
		return ErrPublicationCorrupt
	}
	return nil
}

// PublicationSource is the private transport surface served to Core.
type PublicationSource interface {
	ClaimSource
}
