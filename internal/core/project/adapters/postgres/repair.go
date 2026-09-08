package postgres

import (
	"context"

	"github.com/yangtao121/workos/internal/core/project/adapters/postgres/projectdb"
	"github.com/yangtao121/workos/internal/core/project/domain"
)

func (r *Repository) ResolveRepairTarget(ctx context.Context, owner, project, installation string) (domain.RepairTarget, error) {
	row, err := r.queries.ResolveActiveInstallation(ctx, projectdb.ResolveActiveInstallationParams{OwnerUserID: owner, ProjectID: project, ID: installation})
	value, err := installationFromResolver(row, err)
	if err != nil {
		return domain.RepairTarget{}, err
	}
	if row.ProjectRevision <= 0 {
		return domain.RepairTarget{}, domain.ErrInstallationCorrupt
	}
	return domain.RepairTarget{Installation: value, ProjectRevision: row.ProjectRevision}, nil
}
