// The rebuild state machine driver (ADR-0013 §9). One RunPass call advances
// every live job by a bounded amount; the machine only ever moves forward
// through the durable phases, a cancel lands at the next safe checkpoint,
// and promotion is a single-row compare-and-swap. A crash between passes
// resumes from the stored phase, cursor, and counts — never from zero.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/indexer/ports"
)

// RebuildExecutor drives the durable machine.
type RebuildExecutor struct {
	store     RebuildStore
	driver    RebuildDriver
	now       func() time.Time
	batchSize int
}

func NewRebuildExecutor(store RebuildStore, driver RebuildDriver, batchSize int) (*RebuildExecutor, error) {
	if store == nil || driver.feed == nil || driver.ids == nil {
		return nil, errors.New("rebuild executor requires store and feed driver")
	}
	if batchSize <= 0 || batchSize > 200 {
		batchSize = 100
	}
	if driver.now == nil {
		driver.now = func() time.Time { return time.Now().UTC() }
	}
	return &RebuildExecutor{store: store, driver: driver, now: driver.now, batchSize: batchSize}, nil
}

// Start adjudicates one operator rebuild command: same key + same canonical
// scope replays the stored job; same key + different scope conflicts; a live
// rebuild of the same scope is refused until it terminates.
func (e *RebuildExecutor) Start(ctx context.Context, request RebuildRequest) (RebuildJobView, bool, error) {
	if err := request.Validate(); err != nil {
		return RebuildJobView{}, false, err
	}
	digest := request.RequestDigest()
	return e.store.AdjudicateRebuildRequest(ctx, request.IdempotencyKey, digest,
		func(ctx context.Context) (RebuildJobView, error) {
			generation := e.driver.ids.New()
			jobID := e.driver.ids.New()
			now := e.now().UTC()
			scope := request.Scope
			if err := e.store.CreateGeneration(ctx, generation, scope, request.OwnerUserID, request.ProjectID, now); err != nil {
				return RebuildJobView{}, err
			}
			job := RebuildJobView{
				ID: jobID, Scope: scope,
				OwnerUserID: request.OwnerUserID, ProjectID: request.ProjectID,
				State: "requested", TargetGeneration: generation,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := e.store.CreateRebuildJob(ctx, job, digest); err != nil {
				return RebuildJobView{}, err
			}
			return job, nil
		})
}

// GetJob reads one job view through the durable store.
func (e *RebuildExecutor) GetJob(ctx context.Context, jobID string) (RebuildJobView, error) {
	return e.store.GetRebuildJob(ctx, jobID)
}

// Cancel requests cancellation at the next safe checkpoint. Promoting jobs
// are never canceled: the promotion either lands or the CAS refuses it.
func (e *RebuildExecutor) Cancel(ctx context.Context, jobID string) (bool, error) {
	return e.store.CancelRebuildJob(ctx, jobID, e.now().UTC())
}

// RunPass advances all live jobs one bounded step. It returns the number of
// jobs still live; the composition root calls it in a loop.
func (e *RebuildExecutor) RunPass(ctx context.Context) (int, error) {
	jobs, err := e.store.LiveRebuildJobs(ctx)
	if err != nil {
		return 0, err
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return len(jobs), ctx.Err()
		}
		if err := e.advance(ctx, job); err != nil {
			return len(jobs), err
		}
	}
	return len(jobs), nil
}

func (e *RebuildExecutor) advance(ctx context.Context, job RebuildJobView) error {
	switch job.State {
	case "requested":
		job.State = "snapshotting"
		job.UpdatedAt = e.now().UTC()
		return e.store.SaveRebuildJob(ctx, job)
	case "snapshotting":
		return e.snapshotStep(ctx, job)
	case "catching_up":
		return e.catchUpStep(ctx, job)
	case "validating":
		return e.validateStep(ctx, job)
	case "promoting":
		return e.promoteStep(ctx, job)
	default:
		// Terminal states are filtered by the live query; anything else is
		// stored corruption.
		return ErrInvalidRebuild
	}
}

// snapshotStep pages Core authority into the target generation. The page
// cursor and running counts are persisted per batch, so a restart resumes
// from the cursor.
func (e *RebuildExecutor) snapshotStep(ctx context.Context, job RebuildJobView) error {
	cursor := job.PhaseCursor
	for page := 0; page < maxSnapshotPagesPerPass; page++ {
		sources, next, watermark, err := e.driver.feed.ReconcileSources(ctx, e.batchSize, firstPageOr(cursor))
		if err != nil {
			return err
		}
		for _, source := range sources {
			resolved, resolveErr := e.driver.feed.ResolveSourceContent(
				ctx, source.OwnerUserID, source.ProjectID, source.ArtifactID, source.Digest)
			if resolveErr != nil {
				// An unresolvable source (archived since the page was read)
				// snapshots as a tombstone; anything else fails the pass.
				if !errors.Is(resolveErr, ports.ErrNotFound) {
					return resolveErr
				}
				resolved = ports.ResolvedSource{Verdict: "tombstoned"}
			}
			tombstone := resolved.Content == nil
			apply := SnapshotApply{Source: SnapshotSource{
				OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID,
				ArtifactID: source.ArtifactID, ArtifactType: source.ArtifactType,
				Digest: source.Digest, CreatedAt: source.CreatedAt,
			}, Content: resolved.Content, Title: resolved.Title, TaskID: resolved.SourceTaskID, Tombbed: tombstone}
			if err := e.store.ApplySnapshotSource(ctx, snapshotEffect(apply, e.driver.ids.New()), job.TargetGeneration, snapshotDigest(apply), e.now().UTC()); err != nil {
				return err
			}
			job.AppliedCount++
			job.SourceCount++
		}
		if next == "" {
			// Snapshot complete: the boundary is the authoritative watermark
			// of the final page; live catch-up consumes beyond it.
			job.State = "catching_up"
			job.SnapshotBoundary = watermark
			job.PhaseCursor = ""
			job.UpdatedAt = e.now().UTC()
			return e.store.SaveRebuildJob(ctx, job)
		}
		cursor = next
	}
	job.PhaseCursor = cursor
	job.UpdatedAt = e.now().UTC()
	return e.store.SaveRebuildJob(ctx, job)
}

// catchUpStep waits for a Core-confirmed barrier: the live worker mirrors
// every publication into building generations (writable set), so once Core
// reports no pending outcomes the target has caught up to a drained feed.
func (e *RebuildExecutor) catchUpStep(ctx context.Context, job RebuildJobView) error {
	pending, err := e.driver.feed.CountPending(ctx)
	if err != nil {
		return err
	}
	if pending != 0 {
		// Not yet drained: stay in phase; the next pass re-checks.
		return nil
	}
	job.State = "validating"
	job.UpdatedAt = e.now().UTC()
	return e.store.SaveRebuildJob(ctx, job)
}

// validateStep compares the target generation against a fresh authoritative
// walk: document count and every applied digest must match. Any mismatch
// fails the target generation while the active generation keeps serving.
func (e *RebuildExecutor) validateStep(ctx context.Context, job RebuildJobView) error {
	authoritative := make(map[string]string, job.SourceCount)
	cursor := ""
	for page := 0; page < maxSnapshotPagesPerPass; page++ {
		sources, next, _, err := e.driver.feed.ReconcileSources(ctx, e.batchSize, firstPageOr(cursor))
		if err != nil {
			return err
		}
		for _, source := range sources {
			authoritative[source.ArtifactID] = source.Digest
		}
		if next == "" {
			break
		}
		cursor = next
	}
	ok, err := e.store.ValidateGeneration(ctx, job.TargetGeneration, authoritative)
	if err != nil {
		return err
	}
	if !ok {
		return e.failGeneration(ctx, job, "validation-mismatch")
	}
	job.State = "promoting"
	job.UpdatedAt = e.now().UTC()
	return e.store.SaveRebuildJob(ctx, job)
}

// promoteStep performs the single CAS swap and marks both generations. If
// the expected pointer moved, this job's promotion is refused and the job
// fails without ever producing two active generations.
func (e *RebuildExecutor) promoteStep(ctx context.Context, job RebuildJobView) error {
	current, err := e.driver.feed.ActiveGenerationID(ctx)
	if err != nil {
		return err
	}
	promoted, err := e.store.PromoteCAS(ctx, job.TargetGeneration, current, e.now().UTC())
	if err != nil {
		return err
	}
	now := e.now().UTC()
	if promoted {
		if err := e.store.MarkGeneration(ctx, job.TargetGeneration, "active", now); err != nil {
			return err
		}
		if err := e.store.MarkGeneration(ctx, current, "retired", now); err != nil {
			return err
		}
		job.State = "completed"
		job.UpdatedAt, job.TerminalAt = now, now
		return e.store.SaveRebuildJob(ctx, job)
	}
	return e.failGeneration(ctx, job, "promotion-lost-race")
}

func (e *RebuildExecutor) failGeneration(ctx context.Context, job RebuildJobView, category string) error {
	now := e.now().UTC()
	job.State = "failed"
	job.FailureCategory = category
	job.UpdatedAt, job.TerminalAt = now, now
	if err := e.store.MarkGeneration(ctx, job.TargetGeneration, "failed", now); err != nil {
		return err
	}
	return e.store.SaveRebuildJob(ctx, job)
}

func firstPageOr(cursor string) string {
	if cursor == "" {
		return "v1:0:"
	}
	return cursor
}

func snapshotDigest(apply SnapshotApply) string {
	h := sha256.New()
	fmt.Fprintf(h, "workos.index.rebuild-apply.v1\n%s\n%s\n%s\n%s\n%d",
		apply.Source.ArtifactID, apply.Source.Digest, apply.Source.ArtifactType, apply.Title,
		apply.Source.CreatedAt.UnixMicro())
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func snapshotEffect(apply SnapshotApply, publicationID string) SnapshotEffect {
	return SnapshotEffect{
		OwnerUserID: apply.Source.OwnerUserID, ProjectID: apply.Source.ProjectID,
		ArtifactID: apply.Source.ArtifactID, ArtifactType: apply.Source.ArtifactType,
		Digest: apply.Source.Digest, CreatedAt: apply.Source.CreatedAt,
		Title: apply.Title, Content: apply.Content, TaskID: apply.TaskID,
		Tombstone: apply.Tombbed, PublicationID: publicationID,
	}
}

// SnapshotEffect is the generation-scoped effect the store persists.
type SnapshotEffect struct {
	OwnerUserID   string
	ProjectID     string
	ArtifactID    string
	ArtifactType  string
	Digest        string
	CreatedAt     time.Time
	Title         string
	Content       []byte
	TaskID        string
	Tombstone     bool
	PublicationID string
}
