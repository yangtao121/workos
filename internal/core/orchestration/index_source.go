// Index source authority (ADR-0013): the neutral composition point that
// adapts the Artifact and Project application services to the index feed's
// SourceAuthority port. The feed module never imports other modules or
// queries their tables; every re-verification here goes through the owning
// module's application API or its transaction-scoped liveness check.
package orchestration

import (
	"context"
	"errors"
	"fmt"

	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	"github.com/yangtao121/workos/internal/core/indexfeed/ports"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// projectTxLiveness is the Project adapter's transaction-scoped liveness
// check. The concrete project postgres Repository implements it; wiring it
// here keeps the feed's resolve path inside one database snapshot.
type projectTxLiveness interface {
	ReviewProjectActiveTx(ctx context.Context, tx dbtx.Tx, ownerUserID, projectID string) (bool, error)
}

// IndexSourceAuthority implements ports.SourceAuthority.
type IndexSourceAuthority struct {
	artifacts *artifactapp.Service
	projects  *projectapp.Service
	projectTx projectTxLiveness
}

// NewIndexSourceAuthority composes the feed's source authority from the
// Artifact application service, the Project application service, and the
// Project adapter's transaction-scoped liveness check.
func NewIndexSourceAuthority(artifacts *artifactapp.Service, projects *projectapp.Service, projectTx projectTxLiveness) (*IndexSourceAuthority, error) {
	if artifacts == nil || projects == nil || projectTx == nil {
		return nil, errors.New("index source authority requires artifact service, project service, and project tx liveness")
	}
	return &IndexSourceAuthority{artifacts: artifacts, projects: projects, projectTx: projectTx}, nil
}

// ResolveReviewSource re-verifies one immutable review artifact inside the
// claim's transaction: implemented subtype, exact owner/project/digest
// binding, canonical recomputed content digest, and the project still
// active. Any drift is terminal corruption; an archived project is the
// authoritative tombstone verdict.
func (a *IndexSourceAuthority) ResolveReviewSource(ctx context.Context, tx dbtx.Tx, ownerUserID, projectID, artifactID, expectedDigest string) (ports.VerifiedSource, error) {
	fact, content, err := a.artifacts.GetReview(ctx, ownerUserID, artifactID)
	if err != nil {
		if errors.Is(err, artifactdomain.ErrNotFound) || errors.Is(err, artifactdomain.ErrUnsupported) ||
			errors.Is(err, artifactdomain.ErrCorrupt) {
			// Review artifacts are immutable: a publication whose ref misses
			// its artifact, resolves to a non-review subtype, or fails
			// stored-fact revalidation is terminal drift, not a transient
			// fault.
			return ports.VerifiedSource{}, fmt.Errorf("%w: publication ref does not resolve", ports.ErrSourceCorrupt)
		}
		return ports.VerifiedSource{}, err
	}
	if fact.OwnerUserID != ownerUserID || fact.ProjectID != projectID ||
		fact.Digest != expectedDigest || content.Digest != expectedDigest {
		return ports.VerifiedSource{}, fmt.Errorf("%w: publication facts drifted", ports.ErrSourceCorrupt)
	}
	active, err := a.projectTx.ReviewProjectActiveTx(ctx, tx, ownerUserID, projectID)
	if err != nil {
		return ports.VerifiedSource{}, err
	}
	if !active {
		// The archive lifecycle is authoritative: the project was archived
		// (possibly concurrently with the upsert), so the source must not
		// become searchable.
		return ports.VerifiedSource{}, ports.ErrSourceArchived
	}
	return verifiedSource(fact, content), nil
}

// ResolveSourceContent resolves bounded verified content for a specific
// authoritative artifact outside the claim path (reconciliation repair and
// rebuild snapshotting). Digest-pinned; the project must still be active.
func (a *IndexSourceAuthority) ResolveSourceContent(ctx context.Context, ownerUserID, projectID, artifactID, expectedDigest string) (ports.VerifiedSource, error) {
	fact, content, err := a.artifacts.GetReview(ctx, ownerUserID, artifactID)
	if err != nil {
		if errors.Is(err, artifactdomain.ErrNotFound) || errors.Is(err, artifactdomain.ErrUnsupported) ||
			errors.Is(err, artifactdomain.ErrCorrupt) {
			return ports.VerifiedSource{}, fmt.Errorf("%w: source does not resolve", ports.ErrSourceCorrupt)
		}
		return ports.VerifiedSource{}, err
	}
	if fact.OwnerUserID != ownerUserID || fact.ProjectID != projectID ||
		fact.Digest != expectedDigest || content.Digest != expectedDigest {
		return ports.VerifiedSource{}, fmt.Errorf("%w: source facts drifted", ports.ErrSourceCorrupt)
	}
	project, err := a.projects.Get(ctx, ownerUserID, projectID)
	if err != nil {
		if errors.Is(err, projectdomain.ErrNotFound) || errors.Is(err, projectdomain.ErrInvalid) {
			return ports.VerifiedSource{}, fmt.Errorf("%w: project does not resolve", ports.ErrSourceCorrupt)
		}
		return ports.VerifiedSource{}, err
	}
	if project.ArchivedAt != nil {
		return ports.VerifiedSource{}, ports.ErrSourceArchived
	}
	return verifiedSource(fact, content), nil
}

// ReconcileSources pages authoritative active-project review artifacts in
// stable order. Archived-project rows are dropped here (the archived-projects
// page drives the tombstone side), so a returned page can be shorter than
// the requested size; the cursor — not the row count — decides continuation.
func (a *IndexSourceAuthority) ReconcileSources(ctx context.Context, pageSize int, cursor string) ([]ports.SourceSummary, string, error) {
	page, err := a.artifacts.ReconcileReviewSources(ctx, cursor, pageSize)
	if err != nil {
		return nil, "", err
	}
	type projectKey struct{ owner, project string }
	activeCache := make(map[projectKey]bool, len(page.Sources))
	summaries := make([]ports.SourceSummary, 0, len(page.Sources))
	for _, source := range page.Sources {
		key := projectKey{owner: source.OwnerUserID, project: source.ProjectID}
		active, cached := activeCache[key]
		if !cached {
			active, err = a.projectActive(ctx, source.OwnerUserID, source.ProjectID)
			if err != nil {
				return nil, "", err
			}
			activeCache[key] = active
		}
		if !active {
			continue
		}
		summaries = append(summaries, ports.SourceSummary{
			OwnerUserID:  source.OwnerUserID,
			ProjectID:    source.ProjectID,
			ArtifactID:   source.ArtifactID,
			ArtifactType: source.ArtifactType,
			Digest:       source.Digest,
			CreatedAt:    source.CreatedAt,
		})
	}
	return summaries, page.NextToken, nil
}

// ReconcileArchivedProjects pages archived project scopes in stable order.
func (a *IndexSourceAuthority) ReconcileArchivedProjects(ctx context.Context, pageSize int, cursor string) ([]ports.ArchivedProject, string, error) {
	refs, next, err := a.projects.ReconcileArchivedProjects(ctx, cursor, pageSize)
	if err != nil {
		return nil, "", err
	}
	out := make([]ports.ArchivedProject, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ports.ArchivedProject{
			OwnerUserID: ref.OwnerUserID, ProjectID: ref.ProjectID, ArchivedAt: ref.ArchivedAt,
		})
	}
	return out, next, nil
}

// projectActive checks liveness through the project application service
// outside a feed transaction. A missing project cannot happen for artifact
// facts (FK-stamped), so a miss is corruption, not a miss verdict.
func (a *IndexSourceAuthority) projectActive(ctx context.Context, ownerUserID, projectID string) (bool, error) {
	project, err := a.projects.Get(ctx, ownerUserID, projectID)
	if err != nil {
		if errors.Is(err, projectdomain.ErrNotFound) || errors.Is(err, projectdomain.ErrInvalid) {
			return false, fmt.Errorf("%w: project does not resolve", ports.ErrSourceCorrupt)
		}
		return false, err
	}
	return project.ArchivedAt == nil, nil
}

func verifiedSource(fact artifactdomain.ReviewArtifact, content artifactdomain.NormalizedReviewContent) ports.VerifiedSource {
	return ports.VerifiedSource{
		OwnerUserID:  fact.OwnerUserID,
		ProjectID:    fact.ProjectID,
		ArtifactID:   fact.ID,
		SourceTaskID: fact.SourceTask,
		ArtifactType: fact.Type,
		Digest:       fact.Digest,
		Title:        fact.Title,
		Content:      content.Content,
		CreatedAt:    fact.CreatedAt,
	}
}
