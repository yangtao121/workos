package ports

import (
	"context"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// BuildStore operates only on Registry-owned facts in the coordinator's transaction.
type BuildStore interface {
	GetBuildManifest(context.Context, dbtx.Tx, string, string, string) (string, []byte, error)
	GetBuildSource(context.Context, dbtx.Tx, string, string) (domain.SourceBundle, error)
	CreateBuildSource(context.Context, dbtx.Tx, domain.SourceBundle) (domain.SourceBundle, error)
	FindRepairSource(context.Context, dbtx.Tx, string, string) (string, error)
	InsertRepairSource(context.Context, dbtx.Tx, string, string, string) error
}
