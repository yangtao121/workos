package ports

import (
	"context"
	"errors"

	"github.com/yangtao121/workos/internal/core/project/domain"
)

// ErrStoreUnavailable marks a temporarily unreachable Project store. The
// postgres adapter wraps transient driver failures with it at the port
// boundary; transports map it to a sanitized Unavailable. Invariant and
// constraint failures keep their own verdicts and stay Internal.
var ErrStoreUnavailable = errors.New("project store is temporarily unavailable")

type Repository interface {
	Create(context.Context, domain.Project, string) (domain.Project, error)
	Get(context.Context, string, string) (domain.Project, error)
	List(context.Context, string, string, int, bool) ([]domain.Project, error)
	Update(context.Context, domain.Project, int64) (domain.Project, error)
	Archive(context.Context, string, string, int64) (domain.Project, error)
}
