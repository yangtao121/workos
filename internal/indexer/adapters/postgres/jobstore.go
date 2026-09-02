// Durable repair/reindex job facts over the indexer's own schema. The
// request mapping decides idempotency in the database: same key + same
// canonical request replays the exact first response; same key + different
// request is a stable conflict; validation failures never touch the key.
package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	indexerdb "github.com/yangtao121/workos/internal/indexer/adapters/postgres/indexerdb"
	indexerapp "github.com/yangtao121/workos/internal/indexer/application"
	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type JobStore struct {
	pool    *pgxpool.Pool
	queries *indexerdb.Queries
	ids     ids.Generator
}

func NewJobStore(pool *pgxpool.Pool, generator ids.Generator) (*JobStore, error) {
	if pool == nil || generator == nil {
		return nil, errors.New("indexer job store requires pool and id generator")
	}
	return &JobStore{pool: pool, queries: indexerdb.New(pool), ids: generator}, nil
}

// CreateJob adjudicates the key and persists job + sources + snapshot atomically.
func (s *JobStore) CreateJob(ctx context.Context, command indexerapp.RepairJobCommand) (indexerapp.JobView, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return indexerapp.JobView{}, false, storeError("begin create repair job", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := s.queries.WithTx(tx)

	stored, err := queries.GetIndexJobRequest(ctx, indexerdb.GetIndexJobRequestParams{
		OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return indexerapp.JobView{}, false, storeError("read repair job request", err)
	default:
		if stored.RequestDigest != command.RequestDigest {
			return indexerapp.JobView{}, false, indexerapp.ErrJobConflict
		}
		// Exact replay: re-read the first job's durable view without any new
		// effect.
		var snapshot struct {
			Version int                        `json:"result_version"`
			Job     indexerapp.JobView         `json:"job"`
			Sources []indexerapp.JobSourceView `json:"sources"`
		}
		if err := json.Unmarshal(stored.Result, &snapshot); err != nil || snapshot.Version != 1 {
			return indexerapp.JobView{}, false, domain.ErrCorrupt
		}
		view, _, err := s.GetJobView(ctx, command.OwnerUserID, snapshot.Job.ID)
		if err != nil {
			return indexerapp.JobView{}, false, err
		}
		return view, false, nil
	}

	jobID := s.ids.New()
	if _, err := queries.InsertIndexJob(ctx, indexerdb.InsertIndexJobParams{
		ID: jobID, OwnerUserID: command.OwnerUserID, ProjectID: command.ProjectID,
		CreatedAt: command.Now, UpdatedAt: command.Now,
	}); err != nil {
		return indexerapp.JobView{}, false, storeError("insert repair job", err)
	}
	job := indexerdb.WorkosIndexIndexJob{
		ID: jobID, OwnerUserID: command.OwnerUserID, ProjectID: command.ProjectID,
		State: "pending", CreatedAt: command.Now, UpdatedAt: command.Now,
	}
	for _, source := range command.Sources {
		if err := queries.InsertIndexJobSource(ctx, indexerdb.InsertIndexJobSourceParams{
			JobID: job.ID, ArtifactID: source.ArtifactID, ExpectedDigest: source.Digest, UpdatedAt: command.Now,
		}); err != nil {
			return indexerapp.JobView{}, false, storeError("insert repair job source", err)
		}
	}
	view := jobViewFromRow(job)
	sources, err := s.sourcesFor(ctx, queries, job.ID)
	if err != nil {
		return indexerapp.JobView{}, false, err
	}
	counts, err := s.queries.CountIndexJobSources(ctx, jobID)
	if err != nil {
		return indexerapp.JobView{}, false, storeError("count repair job sources", err)
	}
	view.TotalSources = int(counts.Total)
	view.CompletedSources = int(counts.Completed)
	view.FailedSources = int(counts.Failed)
	snapshot, err := jobSnapshot(view, sources)
	if err != nil {
		return indexerapp.JobView{}, false, err
	}
	if err := queries.InsertIndexJobRequest(ctx, indexerdb.InsertIndexJobRequestParams{
		OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
		RequestDigest: command.RequestDigest, Result: snapshot, CreatedAt: command.Now,
	}); err != nil {
		return indexerapp.JobView{}, false, storeError("insert repair job request", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return indexerapp.JobView{}, false, storeError("commit create repair job", err)
	}
	return view, true, nil
}

// NextRunnableJob claims one pending/running job (database-arbitrated:
// FOR UPDATE SKIP LOCKED) and returns its sources. The job stays running so
// a crashed executor leaves it recoverable.
func (s *JobStore) NextRunnableJob(ctx context.Context, now time.Time) (indexerapp.JobView, []indexerapp.JobSourceView, bool, error) {
	row, err := s.queries.ClaimRunnableIndexJob(ctx, now)
	if errors.Is(err, pgx.ErrNoRows) {
		return indexerapp.JobView{}, nil, false, nil
	}
	if err != nil {
		return indexerapp.JobView{}, nil, false, storeError("claim repair job", err)
	}
	sources, err := s.sourcesFor(ctx, s.queries, row.ID)
	if err != nil {
		return indexerapp.JobView{}, nil, false, err
	}
	return jobViewFromRow(row), sources, true, nil
}

// RecordSourceOutcome persists one source outcome.
func (s *JobStore) RecordSourceOutcome(ctx context.Context, jobID, artifactID, state, outcome string, now time.Time) error {
	return storeError("record repair source outcome", s.queries.UpdateIndexJobSource(ctx, indexerdb.UpdateIndexJobSourceParams{
		JobID: jobID, ArtifactID: artifactID, State: state, Outcome: pgtype.Text{String: outcome, Valid: outcome != ""}, UpdatedAt: now,
	}))
}

// FinishJob transitions the job to its terminal state.
func (s *JobStore) FinishJob(ctx context.Context, jobID, state, failureCategory string, now time.Time) error {
	if state == "failed" {
		err := s.queries.MarkIndexJobFailed(ctx, indexerdb.MarkIndexJobFailedParams{
			ID: jobID, FailureCategory: pgtype.Text{String: failureCategory, Valid: failureCategory != ""}, UpdatedAt: now,
		})
		return storeError("finish repair job", err)
	}
	err := s.queries.UpdateIndexJobState(ctx, indexerdb.UpdateIndexJobStateParams{
		ID: jobID, State: state, UpdatedAt: now,
	})
	return storeError("finish repair job", err)
}

func (s *JobStore) sourcesFor(ctx context.Context, queries *indexerdb.Queries, jobID string) ([]indexerapp.JobSourceView, error) {
	rows, err := queries.GetIndexJobSources(ctx, jobID)
	if err != nil {
		return nil, storeError("list repair job sources", err)
	}
	sources := make([]indexerapp.JobSourceView, 0, len(rows))
	for _, row := range rows {
		sources = append(sources, indexerapp.JobSourceView{
			ArtifactID: row.ArtifactID, Digest: row.ExpectedDigest, State: row.State, Outcome: row.Outcome.String,
		})
	}
	return sources, nil
}

func (s *JobStore) GetJobView(ctx context.Context, ownerUserID, jobID string) (indexerapp.JobView, []indexerapp.JobSourceView, error) {
	row, err := s.queries.GetIndexJob(ctx, indexerdb.GetIndexJobParams{ID: jobID, OwnerUserID: ownerUserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return indexerapp.JobView{}, nil, domain.ErrNotFound
	}
	if err != nil {
		return indexerapp.JobView{}, nil, storeError("read repair job", err)
	}
	sources, err := s.sourcesFor(ctx, s.queries, jobID)
	if err != nil {
		return indexerapp.JobView{}, nil, err
	}
	view := jobViewFromRow(row)
	counts, err := s.queries.CountIndexJobSources(ctx, jobID)
	if err != nil {
		return indexerapp.JobView{}, nil, storeError("count repair job sources", err)
	}
	view.TotalSources = int(counts.Total)
	view.CompletedSources = int(counts.Completed)
	view.FailedSources = int(counts.Failed)
	return view, sources, nil
}

func jobViewFromRow(row indexerdb.WorkosIndexIndexJob) indexerapp.JobView {
	return indexerapp.JobView{
		ID: row.ID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
		State: row.State, FailureCategory: row.FailureCategory.String,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func jobSnapshot(view indexerapp.JobView, sources []indexerapp.JobSourceView) ([]byte, error) {
	return json.Marshal(struct {
		Version int                        `json:"result_version"`
		Job     indexerapp.JobView         `json:"job"`
		Sources []indexerapp.JobSourceView `json:"sources"`
	}{Version: 1, Job: view, Sources: sources})
}
