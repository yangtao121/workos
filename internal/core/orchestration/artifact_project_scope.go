// ArtifactProjectScope adapts the Project application service to the
// Artifact module's neutral project scope port. It is the only bridge in
// this direction: the Artifact module never queries Project tables or
// imports Project adapters, and Project learns nothing about artifacts.
package orchestration

import (
	"context"
	"errors"
	"fmt"

	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

type ArtifactProjectScope struct {
	projects *projectapp.Service
}

func NewArtifactProjectScope(projects *projectapp.Service) (*ArtifactProjectScope, error) {
	if projects == nil {
		return nil, errors.New("artifact project scope requires the project service")
	}
	return &ArtifactProjectScope{projects: projects}, nil
}

// ValidateReadableProject proves projectID is an existing project owned by
// ownerUserID. Archived projects stay readable — review artifacts are
// immutable history, and the Desktop review surface stays consistent after
// archival (ADR-0008). Unknown and foreign projects are the Artifact
// module's sanitized NotFound without existence disclosure.
func (s *ArtifactProjectScope) ValidateReadableProject(ctx context.Context, ownerUserID, projectID string) error {
	project, err := s.projects.Get(ctx, ownerUserID, projectID)
	switch {
	case errors.Is(err, projectdomain.ErrNotFound), errors.Is(err, projectdomain.ErrInvalid):
		return artifactdomain.ErrNotFound
	case err != nil:
		return fmt.Errorf("resolve project for artifact scope: %w", err)
	}
	_ = project
	return nil
}
