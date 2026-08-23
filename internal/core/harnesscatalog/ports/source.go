package ports

import (
	"context"

	"github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
)

type Source interface {
	ListProviders(context.Context) ([]domain.Provider, error)
}
