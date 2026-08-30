package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// TaskStreamFacts is the neutral projection of one task's event stream row
// while it is locked by an active lease inside the coordinator's
// transaction. It carries exactly the facts a Core-minted publication needs;
// the raw task input travels so the coordinator can verify the task actually
// requested the artifact output being materialized.
type TaskStreamFacts struct {
	TaskID            string
	OwnerUserID       string
	ProjectID         string
	ProviderID        string
	State             domain.State
	Input             json.RawMessage
	LastEventSequence int64
}

// TaskCredentialFacts is what the credential-lease coordinator derives from
// an active task lease: whether the task needs a provider credential at all,
// and if so the exact snapshotted credential identity plus the lease's
// current expiry that bounds any derived credential lease.
type TaskCredentialFacts struct {
	Required           bool
	TaskID             string
	OwnerUserID        string
	ProviderID         string
	CredentialID       string
	CredentialRevision int64
	TaskLeaseExpiresAt time.Time
}

// TaskCredentialAuthority is the Agent module's transaction-scoped
// authority for credential lease derivation (ADR-0009). Implementations
// touch only Agent-owned tables; the composition layer owns the transaction
// that also inserts the Credential module's lease row.
type TaskCredentialAuthority interface {
	// ResolveTaskCredential proves taskLeaseID is an active, unexpired
	// execution lease held by workerID on a non-terminal task, locks the
	// lease row, and returns the task's durable credential snapshot facts.
	// A lost, expired, or wrong-worker lease is domain.ErrLeaseLost.
	ResolveTaskCredential(ctx context.Context, tx dbtx.Tx, taskLeaseID, workerID string, now time.Time) (TaskCredentialFacts, error)
	// TaskLeaseExpiry returns the current expiry of the active task lease
	// held by workerID, or found=false once it is finished or expired.
	TaskLeaseExpiry(ctx context.Context, tx dbtx.Tx, taskLeaseID, workerID string, now time.Time) (time.Time, bool, error)
}

// TaskStreamStore is the Agent module's transaction-scoped stream
// coordination port. Implementations write only Agent-owned tables inside
// the caller's transaction; the composition layer owns the transaction
// boundary so no other module ever queries Agent SQL.
type TaskStreamStore interface {
	// LockTaskArtifactStream validates that leaseID/workerID still hold an
	// active, unexpired lease on a non-terminal task and locks the stream,
	// returning the publication-relevant task facts. ErrLeaseLost and
	// ErrTerminal fail closed.
	LockTaskArtifactStream(ctx context.Context, tx dbtx.Tx, leaseID, workerID string, now time.Time) (TaskStreamFacts, error)
	// AppendPublicationEvent persists one Core-minted timeline event (which
	// already carries its server-assigned identity and sequence) and advances
	// the stream's event sequence without changing the task state.
	AppendPublicationEvent(ctx context.Context, tx dbtx.Tx, stream TaskStreamFacts, event domain.Event) error
	// PublicationEventMatches verifies that replay points at the exact
	// durable Agent event originally published. Missing or drifting rows
	// return false; storage failures remain errors for the caller to classify.
	PublicationEventMatches(ctx context.Context, tx dbtx.Tx, expected domain.Event) (bool, error)
}
