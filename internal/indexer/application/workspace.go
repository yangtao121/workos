// Workspace ingestion application service (ADR-0017 §4): owner-bound local
// mounts converge into the search projection through bounded, deterministic
// sync passes. Mount-level failures are recorded as explicit degraded
// states — never silent stops, never stale facts pretending freshness.
package application

import (
	"context"
	"errors"
	"time"

	indexerdomain "github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
)

// ErrWorkspaceStopped reports a sync against an owner-stopped source.
var ErrWorkspaceStopped = errors.New("workspace source is stopped")

// WorkspaceIngestor depends on the durable source store and the (only)
// filesystem boundary adapter.
type WorkspaceIngestor struct {
	store   ports.WorkspaceStore
	mounts  ports.MountReader
	pubs    func() string
	nowFunc func() time.Time
}

// NewWorkspaceIngestor wires the ingestor. passPublications yields the
// monotonic per-file publications (UUIDv7); now may be overridden in tests.
func NewWorkspaceIngestor(store ports.WorkspaceStore, mounts ports.MountReader, passPublications func() string) (*WorkspaceIngestor, error) {
	if store == nil || mounts == nil || passPublications == nil {
		return nil, errServiceWiring("workspace ingestor requires the store, mount reader, and publication source")
	}
	return &WorkspaceIngestor{
		store: store, mounts: mounts, pubs: passPublications,
		nowFunc: func() time.Time { return time.Now().UTC() },
	}, nil
}

// now is the injectable clock.
func (w *WorkspaceIngestor) now() time.Time {
	if w.nowFunc != nil {
		return w.nowFunc()
	}
	return time.Now().UTC()
}

// Register binds (or rebinds) the owner's local directory for one project.
// The root must exist, be a real directory, and resolve to itself — any
// symlink escape is rejected before durable state exists.
func (w *WorkspaceIngestor) Register(ctx context.Context, ownerUserID, projectID, root string) (ports.WorkspaceSource, error) {
	if !indexerdomain.ValidWorkspaceRoot(root) {
		return ports.WorkspaceSource{}, indexerdomain.ErrInvalid
	}
	if err := w.mounts.ValidateRoot(root); err != nil {
		return ports.WorkspaceSource{}, err
	}
	source, err := w.store.InsertWorkspaceSource(ctx, ports.WorkspaceSource{
		ID: w.pubs(), OwnerUserID: ownerUserID, ProjectID: projectID, RootPath: root,
	})
	if err != nil {
		return ports.WorkspaceSource{}, err
	}
	return source, nil
}

// SyncResult is one bounded pass outcome.
type SyncResult struct {
	Source      ports.WorkspaceSource
	Applied     int64
	Tombstoned  int64
	Skipped     int64
	SkipReasons []string
}

// Sync runs one bounded pass: walk the mount, upsert every ingested file,
// tombstone live documents the walk no longer sees, and record the outcome.
// A mount-level failure marks the source degraded with a fixed reason
// category and returns the durable source state.
func (w *WorkspaceIngestor) Sync(ctx context.Context, sourceID string) (SyncResult, error) {
	source, err := w.store.GetWorkspaceSource(ctx, sourceID)
	if err != nil {
		return SyncResult{}, err
	}
	if source.Status == indexerdomain.WorkspaceStopped {
		return SyncResult{}, ErrWorkspaceStopped
	}
	walk, walkErr := w.mounts.Walk(ctx, source.RootPath)
	if walkErr != nil {
		if errors.Is(walkErr, indexerdomain.ErrWorkspaceDegraded) {
			degraded := source
			degraded.Status = indexerdomain.WorkspaceDegraded
			degraded.DegradedReason = indexerdomain.DegradedReadFailed
			var failure *indexerdomain.WorkspaceFailure
			if errors.As(walkErr, &failure) {
				degraded.DegradedReason = failure.Reason
			}
			if statusErr := w.store.SetWorkspaceSourceStatus(ctx, source.ID,
				indexerdomain.WorkspaceDegraded, degraded.DegradedReason, w.now()); statusErr != nil {
				return SyncResult{}, statusErr
			}
			return SyncResult{Source: degraded}, nil
		}
		return SyncResult{}, walkErr
	}
	files := make([]ports.MountFile, 0, len(walk.Files))
	for _, file := range walk.Files {
		files = append(files, ports.MountFile{
			RelPath: file.RelPath, Title: file.Title, Content: file.Content,
			Digest: file.Digest, SourceID: indexerdomain.WorkspaceSourceID(source.OwnerUserID, source.ProjectID, file.RelPath),
		})
	}
	now := w.now()
	applied, tombstoned, convergeErr := w.store.ConvergeWorkspacePass(ctx, source, files, w.pubs, now)
	if convergeErr != nil {
		return SyncResult{}, convergeErr
	}
	reasons := make([]string, 0, len(walk.Skips))
	for _, skip := range walk.Skips {
		reasons = append(reasons, skip.Reason)
	}
	if err := w.store.RecordWorkspaceSync(ctx, source.ID, applied, int64(len(walk.Skips)), tombstoned, now); err != nil {
		return SyncResult{}, err
	}
	updated, err := w.store.GetWorkspaceSource(ctx, source.ID)
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{
		Source: updated, Applied: applied, Tombstoned: tombstoned,
		Skipped: int64(len(walk.Skips)), SkipReasons: reasons,
	}, nil
}

// List returns every bound source.
func (w *WorkspaceIngestor) List(ctx context.Context) ([]ports.WorkspaceSource, error) {
	return w.store.ListWorkspaceSources(ctx)
}
