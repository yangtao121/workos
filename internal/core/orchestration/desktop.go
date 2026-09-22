package orchestration

import (
	"context"
	"errors"

	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	artifactports "github.com/yangtao121/workos/internal/core/artifact/ports"
	desktopdomain "github.com/yangtao121/workos/internal/core/desktop/domain"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	projectports "github.com/yangtao121/workos/internal/core/project/ports"
)

// DesktopReferences bridges owning module application ports; the desktop
// never reads project, installation, session, artifact or Runtime tables.
type DesktopReferences struct {
	Projects interface {
		Get(context.Context, string, string) (projectdomain.Project, error)
	}
	Installations interface {
		ResolveActiveInstallation(context.Context, string, string, string) (projectdomain.Installation, error)
	}
	Sessions interface {
		Get(context.Context, string, string) (agentdomain.Session, error)
	}
	Artifacts interface {
		Get(context.Context, string, string) (artifactdomain.Artifact, error)
	}
	Runtime interface {
		Target(context.Context, string, desktopdomain.Target) error
	}
}

func desktopReferenceError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, projectdomain.ErrNotFound), errors.Is(err, agentdomain.ErrSessionNotFound), errors.Is(err, artifactdomain.ErrNotFound):
		return desktopdomain.ErrNotFound
	case errors.Is(err, projectports.ErrStoreUnavailable), errors.Is(err, agentports.ErrStoreUnavailable), errors.Is(err, artifactports.ErrStoreUnavailable):
		return desktopdomain.ErrUnavailable
	default:
		return err
	}
}
func (r *DesktopReferences) Project(ctx context.Context, owner, id string) error {
	p, err := r.Projects.Get(ctx, owner, id)
	if err != nil {
		return desktopReferenceError(err)
	}
	if p.ID != id || p.OwnerUserID != owner || p.ArchivedAt != nil {
		return desktopdomain.ErrNotFound
	}
	return nil
}
func (r *DesktopReferences) Target(ctx context.Context, owner string, t desktopdomain.Target) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if t.ProjectID != "" {
		if err := r.Project(ctx, owner, t.ProjectID); err != nil {
			return err
		}
	}
	switch t.ResourceKind {
	case "":
		return nil
	case "app":
		_, err := r.Installations.ResolveActiveInstallation(ctx, owner, t.ProjectID, t.ResourceID)
		return desktopReferenceError(err)
	case "session":
		s, err := r.Sessions.Get(ctx, owner, t.ResourceID)
		if err != nil {
			return desktopReferenceError(err)
		}
		if s.ID != t.ResourceID || s.ProjectID != t.ProjectID || s.OwnerUserID != owner {
			return desktopdomain.ErrNotFound
		}
		return nil
	case "artifact":
		a, err := r.Artifacts.Get(ctx, owner, t.ResourceID)
		if err != nil {
			return desktopReferenceError(err)
		}
		if a.ID != t.ResourceID || a.OwnerUserID != owner || (a.ProjectID != "" && a.ProjectID != t.ProjectID) {
			return desktopdomain.ErrNotFound
		}
		return nil
	case "workload", "preview":
		return r.Runtime.Target(ctx, owner, t)
	default:
		return desktopdomain.ErrInvalid
	}
}
