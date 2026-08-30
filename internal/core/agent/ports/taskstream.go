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
