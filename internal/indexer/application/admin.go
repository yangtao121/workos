// The local admin surface (ADR-0013 §8): safe operational facts for
// workosctl over the indexer's Unix admin socket. Status carries
// generations, phases, bounded counts, and watermarks — never queries,
// titles, excerpts, content, owner display names, credentials, or tokens.
package application

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/indexer/domain"
)

// AdminService serves the local IndexAdminService contract.
type AdminService struct {
	rebuild *RebuildExecutor
	fresh   *IngestionService
	proj    ActiveGenerationReader
}

// ActiveGenerationReader reports the generation every search reads.
type ActiveGenerationReader interface {
	ActiveGenerationID(ctx context.Context) (string, error)
	ActiveGenerationStatus(ctx context.Context) (domain.GenerationStatus, error)
}

func NewAdminService(rebuild *RebuildExecutor, fresh *IngestionService, proj ActiveGenerationReader) (*AdminService, error) {
	if rebuild == nil || fresh == nil || proj == nil {
		return nil, errorsNew("admin service requires rebuild, freshness, and projection")
	}
	return &AdminService{rebuild: rebuild, fresh: fresh, proj: proj}, nil
}

// IndexStatus is the bounded status projection.
type IndexStatus struct {
	ActiveGeneration domain.GenerationStatus
	ActiveRebuild    *RebuildJobView
	CatchingUp       bool
	Pending          int64
	IndexedThrough   time.Time
	LastIndexedAt    time.Time
}

// Status composes freshness facts with any live rebuild.
func (s *AdminService) Status(ctx context.Context) (IndexStatus, error) {
	generation, err := s.proj.ActiveGenerationStatus(ctx)
	if err != nil {
		return IndexStatus{}, err
	}
	fresh, err := s.fresh.Freshness(ctx)
	if err != nil {
		return IndexStatus{}, err
	}
	status := IndexStatus{
		ActiveGeneration: generation,
		CatchingUp:       !fresh.CaughtUp,
		Pending:          fresh.PendingPublications,
		IndexedThrough:   fresh.IndexedThrough,
		LastIndexedAt:    fresh.LastIndexedAt,
	}
	jobs, err := s.rebuild.store.LiveRebuildJobs(ctx)
	if err != nil {
		return IndexStatus{}, err
	}
	if len(jobs) > 0 {
		live := jobs[0]
		status.ActiveRebuild = &live
	}
	return status, nil
}

// StartRebuild adjudicates and starts one operator rebuild.
func (s *AdminService) StartRebuild(ctx context.Context, request RebuildRequest) (RebuildJobView, bool, error) {
	return s.rebuild.Start(ctx, request)
}

// GetRebuildJob reads one job view.
func (s *AdminService) GetRebuildJob(ctx context.Context, jobID string) (RebuildJobView, error) {
	return s.rebuild.GetJob(ctx, jobID)
}

// CancelRebuildJob requests cancellation at the next safe checkpoint.
func (s *AdminService) CancelRebuildJob(ctx context.Context, jobID string) (bool, error) {
	return s.rebuild.Cancel(ctx, jobID)
}

func errorsNew(message string) error { return &adminWiringError{message} }

type adminWiringError struct{ message string }

func (e *adminWiringError) Error() string { return e.message }
