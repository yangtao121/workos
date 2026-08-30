// Transaction-scoped stream coordination for Core-minted artifact
// publication. These methods write only Agent-owned tables (agent task rows
// and the workos_events event stream) inside the composition layer's shared
// transaction; the Artifact module's rows are written by its own adapter
// within the same transaction.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	agentdb "github.com/yangtao121/workos/internal/core/agent/adapters/postgres/agentdb"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// LockTaskArtifactStream validates the active lease and locks the task event
// stream inside the coordinator's transaction, returning the facts a
// Core-minted publication needs. A lost, expired, or wrong-worker lease is
// ErrLeaseLost; a terminal task is ErrTerminal — neither may materialize.
func (r *Repository) LockTaskArtifactStream(ctx context.Context, tx dbtx.Tx, leaseID, workerID string, now time.Time) (agentports.TaskStreamFacts, error) {
	leaseUUID, err := requiredUUID(leaseID)
	if err != nil {
		return agentports.TaskStreamFacts{}, agentdomain.ErrLeaseLost
	}
	stream, err := r.queries.WithTx(tx).LockTaskArtifactStream(ctx, agentdb.LockTaskArtifactStreamParams{
		LeaseID: leaseUUID, LockedBy: text(workerID), LockedUntil: timestamp(now),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return agentports.TaskStreamFacts{}, agentdomain.ErrLeaseLost
	}
	if err != nil {
		return agentports.TaskStreamFacts{}, fmt.Errorf("lock task artifact stream: %w", err)
	}
	if agentdomain.State(stream.State).Terminal() {
		return agentports.TaskStreamFacts{}, agentdomain.ErrTerminal
	}
	return agentports.TaskStreamFacts{
		TaskID:            stream.ID,
		OwnerUserID:       stream.OwnerUserID,
		ProjectID:         uuidTextValue(stream.ProjectID),
		ProviderID:        stream.ProviderID,
		State:             agentdomain.State(stream.State),
		Input:             stream.Input,
		LastEventSequence: stream.LastEventSequence,
	}, nil
}

// AppendPublicationEvent persists one Core-minted timeline event and advances
// the stream's sequence without touching the task state, inside the
// coordinator's transaction.
func (r *Repository) AppendPublicationEvent(ctx context.Context, tx dbtx.Tx, stream agentports.TaskStreamFacts, event agentdomain.Event) error {
	if event.TaskID != stream.TaskID || event.Sequence != stream.LastEventSequence+1 {
		return fmt.Errorf("publication event identity does not match the locked stream: %w", agentdomain.ErrInvalid)
	}
	queries := r.queries.WithTx(tx)
	if err := addEventMetadata(&event); err != nil {
		return err
	}
	if err := insertEvent(ctx, queries, event); err != nil {
		return err
	}
	if err := queries.AdvanceTaskPublicationSequence(ctx, agentdb.AdvanceTaskPublicationSequenceParams{
		Sequence: event.Sequence, UpdatedAt: timestamp(event.OccurredAt), TaskID: stream.TaskID,
	}); err != nil {
		return fmt.Errorf("advance publication sequence: %w", err)
	}
	return nil
}

// PublicationEventMatches proves that an Artifact output replay references
// the exact Agent-owned event committed by the first materialization. The
// output mapping is an idempotency reference, not a substitute authority for
// the timeline row, so missing or drifting event facts fail closed.
func (r *Repository) PublicationEventMatches(ctx context.Context, tx dbtx.Tx, expected agentdomain.Event) (bool, error) {
	row, err := r.queries.WithTx(tx).GetTaskPublicationEvent(ctx, agentdb.GetTaskPublicationEventParams{
		EventID: expected.ID, TaskID: expected.TaskID, EventSequence: expected.Sequence,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storeError("query task publication event", err)
	}
	canonical := expected
	if err := addEventMetadata(&canonical); err != nil {
		return false, err
	}
	return row.ID == canonical.ID && row.StreamID == canonical.TaskID &&
		row.Sequence == canonical.Sequence && row.EventType == canonical.EventType &&
		row.OccurredAt.Time.Equal(canonical.OccurredAt) && sameJSON(row.Payload, canonical.Payload), nil
}

// ResolveTaskCredential proves the task lease is active and held by this
// worker, locks the lease row against a concurrent finish, and returns the
// task's durable credential snapshot facts (ADR-0009). Required is derived
// from the snapshot's presence — never from caller input — so a worker can
// never select owner, provider, credential ID, or revision.
func (r *Repository) ResolveTaskCredential(ctx context.Context, tx dbtx.Tx, taskLeaseID, workerID string, now time.Time) (agentports.TaskCredentialFacts, error) {
	leaseUUID, err := requiredUUID(taskLeaseID)
	if err != nil {
		return agentports.TaskCredentialFacts{}, agentdomain.ErrLeaseLost
	}
	row, err := r.queries.WithTx(tx).LockTaskCredentialLeaseFacts(ctx, agentdb.LockTaskCredentialLeaseFactsParams{
		LeaseID: leaseUUID, LockedBy: text(workerID), LockedUntil: timestamp(now),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return agentports.TaskCredentialFacts{}, agentdomain.ErrLeaseLost
	}
	if err != nil {
		return agentports.TaskCredentialFacts{}, fmt.Errorf("lock task credential lease facts: %w", err)
	}
	facts := agentports.TaskCredentialFacts{
		TaskID: row.ID, OwnerUserID: row.OwnerUserID, ProviderID: row.ProviderID,
		TaskLeaseExpiresAt: row.LockedUntil.Time,
	}
	if row.CredentialID.Valid {
		facts.Required = true
		facts.CredentialID = uuid.UUID(row.CredentialID.Bytes).String()
		facts.CredentialRevision = row.CredentialRevision.Int64
	}
	return facts, nil
}

// TaskLeaseExpiry returns the current expiry of the active task lease held
// by this worker, locking the lease row so the credential renewal commits
// against exactly the expiry it observed.
func (r *Repository) TaskLeaseExpiry(ctx context.Context, tx dbtx.Tx, taskLeaseID, workerID string, now time.Time) (time.Time, bool, error) {
	leaseUUID, err := requiredUUID(taskLeaseID)
	if err != nil {
		return time.Time{}, false, agentdomain.ErrLeaseLost
	}
	expiry, err := r.queries.WithTx(tx).GetTaskLeaseExpiry(ctx, agentdb.GetTaskLeaseExpiryParams{
		LeaseID: leaseUUID, LockedBy: text(workerID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read task lease expiry: %w", err)
	}
	if !expiry.Valid || expiry.Time.Before(now) {
		return time.Time{}, false, nil
	}
	return expiry.Time, true, nil
}

func sameJSON(left, right []byte) bool {
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func uuidTextValue(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}
