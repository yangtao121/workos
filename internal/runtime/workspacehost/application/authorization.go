package application

import (
	"context"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/ports"
)

// AuthorizedDirectory joins Core's current grant to Runtime's registered root.
// Its validator pins both binding and revision for the lifetime of a process.
func (s *Service) AuthorizedDirectory(ctx context.Context, a ports.Authorizer, owner, project string) (string, bool, func(context.Context) error, error) {
	if s == nil || a == nil {
		return "", false, nil, domain.ErrUnavailable
	}
	grant, err := a.Resolve(ctx, owner, project)
	if err != nil {
		return "", false, nil, err
	}
	source, ok := s.Resolve(owner, project)
	if !ok || source.ID != grant.SourceID {
		return "", false, nil, domain.ErrDenied
	}
	validate := func(ctx context.Context) error {
		current, err := a.Resolve(ctx, owner, project)
		if err != nil {
			return err
		}
		if current != grant {
			return domain.ErrDenied
		}
		return nil
	}
	return source.Path, source.ReadOnly || grant.ReadOnly, validate, nil
}
