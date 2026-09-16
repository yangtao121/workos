package ports

import (
	"context"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"time"
)

type InteractionStore interface {
	CreateInteraction(context.Context, domain.ExecutionInteraction) (domain.ExecutionInteraction, error)
	GetInteraction(context.Context, string, string) (domain.ExecutionInteraction, error)
	ListInteractions(context.Context, string, string) ([]domain.ExecutionInteraction, error)
	DecideInteraction(context.Context, domain.ExecutionInteraction, time.Time) (domain.ExecutionInteraction, error)
	ExpireInteraction(context.Context, string, string) error
}
type InteractionAuthority interface {
	ValidateInteraction(context.Context, string, string) error
}
