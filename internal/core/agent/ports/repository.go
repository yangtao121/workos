package ports

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/core/agent/domain"
)

type Repository interface {
	Create(context.Context, domain.Task, string) (domain.Task, error)
	Get(context.Context, string, string) (domain.Task, error)
	GetByIdempotency(context.Context, string, string) (domain.Task, error)
	List(context.Context, string, string, string, int) ([]domain.Task, error)
	Cancel(context.Context, string, string, string, time.Time) (domain.Task, *domain.Event, error)
	ListEvents(context.Context, string, string, int64, int) ([]domain.Event, error)
	Claim(context.Context, string, time.Duration, string, time.Time) (*domain.Lease, error)
	Renew(context.Context, string, string, time.Duration, time.Time) (time.Time, bool, error)
	AppendEvent(context.Context, string, string, domain.Event, domain.State, string, string, time.Time) (domain.Event, error)
	FinishLease(context.Context, string, string, time.Time) error
}
