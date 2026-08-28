package ports

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/core/agent/domain"
)

// AppTaskProvenance is the durable App fact bound to one bridge-created task:
// which app installation of which owner used which client key, and the
// canonical request digest that adjudicates replays.
type AppTaskProvenance struct {
	// TaskIdempotencyKey is the opaque unique value stored in the task row's
	// own key column; App adjudication lives in the mapping below.
	TaskIdempotencyKey   string
	AppInstanceID        string
	ClientIdempotencyKey string
	RequestDigest        string
}

// AppTaskRequestRecord is one persisted App task mapping read.
type AppTaskRequestRecord struct {
	RequestDigest string
	TaskID        string
	ProjectID     string
}

type Repository interface {
	Create(context.Context, domain.Task, string) (domain.Task, error)
	// CreateForApp inserts the task, the App provenance mapping, and the
	// task outbox row in one transaction. A concurrent same-key mapping
	// winner replays or conflicts exactly like the user path; the losing
	// transaction rolls back with no orphan task, mapping, or outbox row.
	CreateForApp(context.Context, domain.Task, AppTaskProvenance) (domain.Task, error)
	Get(context.Context, string, string) (domain.Task, error)
	GetByIdempotency(context.Context, string, string) (domain.Task, error)
	// GetAppTaskRequest reads one consumed (owner, app instance, client key)
	// mapping for replay adjudication.
	GetAppTaskRequest(context.Context, string, string, string) (AppTaskRequestRecord, bool, error)
	// GetAppTaskByTask reads the mapping that proves one task was created by
	// one app installation of one owner (watch provenance check).
	GetAppTaskByTask(context.Context, string, string, string) (AppTaskRequestRecord, bool, error)
	List(context.Context, string, string, string, int) ([]domain.Task, error)
	Cancel(context.Context, string, string, string, time.Time) (domain.Task, *domain.Event, error)
	ListEvents(context.Context, string, string, int64, int) ([]domain.Event, error)
	Claim(context.Context, string, time.Duration, string, time.Time) (*domain.Lease, error)
	Renew(context.Context, string, string, time.Duration, time.Time) (time.Time, bool, error)
	AppendEvent(context.Context, string, string, domain.Event, domain.State, string, string, time.Time) (domain.Event, error)
	FinishLease(context.Context, string, string, time.Time) error
}
