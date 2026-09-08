package application

import (
	"context"
	"errors"

	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
)

type RepairTargets struct{ source ports.RepairTargetSource }

func NewRepairTargets(source ports.RepairTargetSource) (*RepairTargets, error) {
	if source == nil {
		return nil, errors.New("repair targets require an installation source")
	}
	return &RepairTargets{source: source}, nil
}
func (s *RepairTargets) Get(ctx context.Context, owner, project, installation string) (domain.RepairTarget, error) {
	for _, id := range []string{owner, project, installation} {
		if !domain.ValidStoredInstallationUUID(id) {
			return domain.RepairTarget{}, domain.ErrInvalid
		}
	}
	return s.source.ResolveRepairTarget(ctx, owner, project, installation)
}
