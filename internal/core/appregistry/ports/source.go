package ports

import (
	"context"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
)

type SourceRepository interface {
	CreateSource(context.Context, domain.SourceBundle) (domain.SourceBundle, error)
	GetSource(ctx context.Context, ownerUserID, bundleID string) (domain.SourceBundle, error)
}
