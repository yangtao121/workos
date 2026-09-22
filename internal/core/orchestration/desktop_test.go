package orchestration

import (
	"context"
	"errors"
	"testing"

	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	desktopdomain "github.com/yangtao121/workos/internal/core/desktop/domain"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type desktopProjects struct{ p projectdomain.Project }

func (r desktopProjects) Get(context.Context, string, string) (projectdomain.Project, error) {
	return r.p, nil
}

type desktopSessions struct{ s agentdomain.Session }

func (r desktopSessions) Get(context.Context, string, string) (agentdomain.Session, error) {
	return r.s, nil
}

type desktopArtifacts struct{ a artifactdomain.Artifact }

func (r desktopArtifacts) Get(context.Context, string, string) (artifactdomain.Artifact, error) {
	return r.a, nil
}

type desktopInstallations struct{ err error }

func (r desktopInstallations) ResolveActiveInstallation(context.Context, string, string, string) (projectdomain.Installation, error) {
	return projectdomain.Installation{}, r.err
}
func TestDesktopReferencesRejectForeignSessionArtifactAndUninstalledApp(t *testing.T) {
	g := ids.UUIDv7{}
	owner, p, q, resource := g.New(), g.New(), g.New(), g.New()
	refs := &DesktopReferences{Projects: desktopProjects{projectdomain.Project{ID: p, OwnerUserID: owner}}, Sessions: desktopSessions{agentdomain.Session{ID: resource, ProjectID: q, OwnerUserID: owner}}, Artifacts: desktopArtifacts{artifactdomain.Artifact{ID: resource, ProjectID: q, OwnerUserID: owner}}, Installations: desktopInstallations{err: projectdomain.ErrNotFound}}
	ctx := context.Background()
	for _, target := range []desktopdomain.Target{{Kind: "agent-sessions", ProjectID: p, ResourceKind: "session", ResourceID: resource}, {Kind: "artifact-viewer", ProjectID: p, ResourceKind: "artifact", ResourceID: resource}, {Kind: "app-surface", ProjectID: p, ResourceKind: "app", ResourceID: resource}} {
		if err := refs.Target(ctx, owner, target); !errors.Is(err, desktopdomain.ErrNotFound) {
			t.Fatalf("foreign/removed ref accepted %+v: %v", target, err)
		}
	}
	refs.Sessions = desktopSessions{agentdomain.Session{ID: resource, ProjectID: p, OwnerUserID: owner}}
	if err := refs.Target(ctx, owner, desktopdomain.Target{Kind: "agent-sessions", ProjectID: p, ResourceKind: "session", ResourceID: resource}); err != nil {
		t.Fatal("valid canonical session rejected", err)
	}
	refs.Projects = desktopProjects{projectdomain.Project{ID: p, OwnerUserID: g.New()}}
	if err := refs.Project(ctx, owner, p); !errors.Is(err, desktopdomain.ErrNotFound) {
		t.Fatal("owner mismatch accepted", err)
	}
}
