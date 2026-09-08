package postgres

import (
	"context"
	"github.com/yangtao121/workos/internal/core/appregistry/adapters/postgres/appdb"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

func (r *Repository) GetBuildManifest(ctx context.Context, tx dbtx.Tx, owner, appID, version string) (string, []byte, error) {
	row, err := r.queries.WithTx(tx).GetAppBuildManifest(ctx, appdb.GetAppBuildManifestParams{OwnerUserID: owner, AppID: appID, Version: version})
	if err != nil {
		return "", nil, appVersionError("read app build manifest", err)
	}
	return row.ManifestDigest, row.CanonicalManifest, nil
}
func (r *Repository) GetBuildSource(ctx context.Context, tx dbtx.Tx, owner, id string) (domain.SourceBundle, error) {
	scoped := &Repository{queries: r.queries.WithTx(tx)}
	return scoped.GetSource(ctx, owner, id)
}
func (r *Repository) CreateBuildSource(ctx context.Context, tx dbtx.Tx, source domain.SourceBundle) (domain.SourceBundle, error) {
	scoped := &Repository{queries: r.queries.WithTx(tx)}
	return scoped.CreateSource(ctx, source)
}
func (r *Repository) FindRepairSource(ctx context.Context, tx dbtx.Tx, owner, taskID string) (string, error) {
	id, err := r.queries.WithTx(tx).GetRepairSourceCandidate(ctx, appdb.GetRepairSourceCandidateParams{OwnerUserID: owner, TaskID: taskID})
	if err != nil {
		return "", appVersionError("read repair source", err)
	}
	return id, nil
}
func (r *Repository) InsertRepairSource(ctx context.Context, tx dbtx.Tx, owner, taskID, sourceID string) error {
	if err := r.queries.WithTx(tx).InsertRepairSourceCandidate(ctx, appdb.InsertRepairSourceCandidateParams{OwnerUserID: owner, TaskID: taskID, SourceBundleID: sourceID}); err != nil {
		return storeError("insert repair source", err)
	}
	return nil
}
