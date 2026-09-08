package ports

import (
	"context"
	"github.com/yangtao121/workos/internal/core/project/domain"
)

type RepairTargetSource interface {
	ResolveRepairTarget(ctx context.Context, ownerUserID, projectID, installationID string) (domain.RepairTarget, error)
}
