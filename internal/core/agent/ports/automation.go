package ports

import (
	"context"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"time"
)

// Every operation rechecks the live parent lease. Scope facts supplied to
// Acquire are Core-derived project binding facts, never model arguments.
type DelegationRepository interface {
	AcquireDelegation(context.Context, string, string, domain.Delegation, time.Time) (domain.Delegation, error)
	GetTaskDelegation(context.Context, string, string, string, time.Time) (domain.Delegation, error)
	UpdateTaskDelegation(context.Context, string, string, domain.Delegation, time.Time) (domain.Delegation, error)
}

// DelegationPublicationStore authorizes a result while the coordinator holds
// the parent task stream lock. Artifact never reads Agent tables directly.
type DelegationPublicationStore interface {
	AuthorizeDelegationPublication(context.Context, dbtx.Tx, TaskStreamFacts, string) error
}
