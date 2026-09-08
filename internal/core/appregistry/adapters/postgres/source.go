package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/yangtao121/workos/internal/core/appregistry/adapters/postgres/appdb"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
)

func (r *Repository) CreateSource(ctx context.Context, bundle domain.SourceBundle) (domain.SourceBundle, error) {
	if err := domain.ValidateSourceBundle(bundle); err != nil {
		return domain.SourceBundle{}, err
	}
	files, err := json.Marshal(bundle.Files)
	if err != nil {
		return domain.SourceBundle{}, domain.ErrInvalid
	}
	inserted, err := r.queries.InsertAppSourceBundle(ctx, appdb.InsertAppSourceBundleParams{ID: bundle.ID, OwnerUserID: bundle.OwnerUserID, IdempotencyKey: bundle.IdempotencyKey, Digest: bundle.Digest, Files: files, TotalSizeBytes: bundle.TotalSizeBytes, CreatedAt: timestamp(bundle.CreatedAt)})
	if err != nil {
		return domain.SourceBundle{}, storeError("create app source", err)
	}
	if inserted == 1 {
		return bundle, nil
	}
	row, err := r.queries.GetAppSourceBundleByKey(ctx, appdb.GetAppSourceBundleByKeyParams{OwnerUserID: bundle.OwnerUserID, IdempotencyKey: bundle.IdempotencyKey})
	stored, err := sourceFromDB(appdb.GetAppSourceBundleRow(row), err)
	if err != nil {
		return domain.SourceBundle{}, err
	}
	if stored.Digest != bundle.Digest {
		return domain.SourceBundle{}, domain.ErrIdempotencyConflict
	}
	return stored, nil
}
func (r *Repository) GetSource(ctx context.Context, owner, id string) (domain.SourceBundle, error) {
	row, err := r.queries.GetAppSourceBundle(ctx, appdb.GetAppSourceBundleParams{OwnerUserID: owner, ID: id})
	return sourceFromDB(row, err)
}
func sourceFromDB(row appdb.GetAppSourceBundleRow, err error) (domain.SourceBundle, error) {
	if err != nil {
		return domain.SourceBundle{}, appVersionError("read app source", err)
	}
	if !row.CreatedAt.Valid || len(row.Files) > 1024*1024 {
		return domain.SourceBundle{}, domain.ErrSourceCorrupt
	}
	bundle := domain.SourceBundle{ID: row.ID, OwnerUserID: row.OwnerUserID, IdempotencyKey: row.IdempotencyKey, Digest: row.Digest, TotalSizeBytes: row.TotalSizeBytes, CreatedAt: row.CreatedAt.Time.UTC()}
	decoder := json.NewDecoder(bytes.NewReader(row.Files))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle.Files); err != nil {
		return domain.SourceBundle{}, domain.ErrSourceCorrupt
	}
	if decoder.Decode(new(any)) != io.EOF {
		return domain.SourceBundle{}, domain.ErrSourceCorrupt
	}
	if err := domain.ValidateSourceBundle(bundle); err != nil {
		return domain.SourceBundle{}, err
	}
	return bundle, nil
}
