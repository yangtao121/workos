// Bounded reconciliation (ADR-0013 §3): additive digest repair over the
// authoritative active-source pages plus archived-project tombstone
// convergence. The pass is incremental by design — the authoritative full
// compare belongs to the shadow-generation rebuild — so a hostile history
// row cannot block unrelated projects and a missed document is repaired on
// the next pass or by the next rebuild.
package application

import (
	"context"
	"errors"

	"github.com/yangtao121/workos/internal/indexer/ports"
	ids "github.com/yangtao121/workos/internal/platform/ids"
)

// Reconcile walks one bounded pass: active-source pages (missing or drifted
// documents are re-resolved digest-pinned and applied through the same path
// as live publications), then archived-project pages (applied as tombstone
// effects). It stops at the first page that cannot be read; the next pass
// resumes from the authoritative pages themselves.
func Reconcile(ctx context.Context, feed ports.CoreFeedClient, projection ports.ProjectionRepository, generator ids.Generator, pageSize int) error {
	if _, err := projection.EnsureBootstrapGeneration(ctx, timeNow()); err != nil {
		return err
	}
	cursor := ""
	for page := 0; page < maxReconcilePages; page++ {
		sources, next, _, err := feed.ReconcileSources(ctx, pageSize, cursor)
		if err != nil {
			return err
		}
		for _, source := range sources {
			if err := reconcileSource(ctx, feed, projection, generator, source); err != nil {
				return err
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	cursor = ""
	for page := 0; page < maxReconcilePages; page++ {
		projects, next, err := feed.ReconcileArchivedProjects(ctx, pageSize, cursor)
		if err != nil {
			return err
		}
		for _, project := range projects {
			tombstone := ports.ResolvedSource{
				Verdict:       "tombstoned",
				Operation:     "project.tombstone",
				OwnerUserID:   project.OwnerUserID,
				ProjectID:     project.ProjectID,
				PublicationID: generator.New(),
				OccurredAt:    project.ArchivedAt,
			}
			if err := projection.ApplyResolvedSource(ctx, tombstone, "tombstoned", reconcileTombstoneDigest(tombstone), timeNow()); err != nil {
				if errors.Is(err, ports.ErrStoreUnavailable) {
					return err
				}
				return err
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return nil
}

const maxReconcilePages = 100

func reconcileSource(ctx context.Context, feed ports.CoreFeedClient, projection ports.ProjectionRepository, generator ids.Generator, source ports.ReconcileSource) error {
	status, err := projection.DocumentStatus(ctx, source.OwnerUserID, source.ProjectID, source.ArtifactID)
	if err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return err
		}
		return err
	}
	if status.Known && status.Digest == source.Digest && !status.Tombstoned {
		return nil
	}
	resolved, err := feed.ResolveSourceContent(ctx, source.OwnerUserID, source.ProjectID, source.ArtifactID, source.Digest)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) || errors.Is(err, ports.ErrLeaseStale) {
			// The page and the row disagree: leave it to the next pass or
			// rebuild rather than guessing.
			return nil
		}
		return err
	}
	resolved.PublicationID = generator.New()
	resolved.OccurredAt = timeNow()
	resolved.Operation = "review-artifact.upsert"
	resolved.Verdict = "resolved"
	if err := validateResolvedForApply(resolved); err != nil {
		return err
	}
	return projection.ApplyResolvedSource(ctx, resolved, "applied", reconcileApplyDigest(resolved), timeNow())
}

func reconcileTombstoneDigest(source ports.ResolvedSource) string {
	return digestOf("tombstone", source.OwnerUserID, source.ProjectID, source.PublicationID)
}

func reconcileApplyDigest(source ports.ResolvedSource) string {
	return digestOf("apply", source.OwnerUserID, source.ProjectID, source.ArtifactID, source.Digest)
}
