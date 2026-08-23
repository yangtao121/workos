package ports

import (
	"context"

	"github.com/yangtao121/workos/internal/core/project/domain"
)

type Repository interface {
	Create(context.Context, domain.Project, string) (domain.Project, error)
	Get(context.Context, string, string) (domain.Project, error)
	List(context.Context, string, string, int, bool) ([]domain.Project, error)
	Update(context.Context, domain.Project, int64) (domain.Project, error)
	Archive(context.Context, string, string, int64) (domain.Project, error)
}
