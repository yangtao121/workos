//go:build integration

package integration_test

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/indexer/adapters/postgres"
	"github.com/yangtao121/workos/internal/indexer/application"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/tests/fixtures/embedding"
)

func newModelProjection(pool *pgxpool.Pool, generator ids.Generator) (*application.ModelProjection, error) {
	repo, err := postgres.New(pool, generator, embedding.Fingerprint)
	if err != nil {
		return nil, err
	}
	return application.NewModelProjection(repo, embedding.Model{})
}
func newModelRebuildStore(pool *pgxpool.Pool, generator ids.Generator) (*application.ModelRebuildStore, error) {
	store, err := postgres.NewRebuildStore(pool, generator, embedding.Fingerprint)
	if err != nil {
		return nil, err
	}
	return application.NewModelRebuildStore(store, embedding.Model{})
}
