package ports

import (
	"context"

	"github.com/yangtao121/workos/internal/core/desktop/domain"
)

// References delegates ownership and liveness to the owning modules.
// Only definitive ErrNotFound permits pruning; outages never delete state.
type References interface {
	Project(context.Context, string, string) error
	Target(context.Context, string, domain.Target) error
}
type Transaction interface {
	State() domain.State
	Request(context.Context, string) (string, bool, error)
	Remember(context.Context, string, string) error
	Save(context.Context, domain.State) error
	Events(context.Context, int64) ([]domain.State, error)
}

// Locked serializes all commands for one owner across processes and commits
// the state, event and idempotency key atomically. Failure rolls everything back.
type Store interface {
	Locked(context.Context, string, func(Transaction) error) error
}
