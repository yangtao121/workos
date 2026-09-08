// Package postgres persists the Build/Test job ledger (ADR-0026).
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/runtime/buildtest/adapters/postgres/buildtestdb"
	"github.com/yangtao121/workos/internal/runtime/buildtest/domain"
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *buildtestdb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: buildtestdb.New(pool)}
}

func transient(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return domain.ErrStoreUnavailable
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func int4(value *int32) pgtype.Int4 {
	if value == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *value, Valid: true}
}

func (r *Repository) InsertJob(ctx context.Context, job domain.Job) (string, bool, error) {
	encoded, digest, err := domain.CanonicalPayload(job.Payload)
	if err != nil {
		return "", false, domain.ErrInvalid
	}
	inserted, err := r.queries.InsertBuildJob(ctx, buildtestdb.InsertBuildJobParams{
		ID: job.ID, TaskID: job.TaskID, IncidentID: job.IncidentID,
		OwnerUserID: job.OwnerUserID, ProjectID: job.ProjectID, InstallationID: job.InstallationID,
		InputDigest: digest, SourceBundleID: job.SourceBundleID, SourceDigest: job.SourceDigest,
		ManifestDigest: job.ManifestDigest, BaseImage: job.BaseImage,
		Payload: encoded, CreatedAt: job.CreatedAt,
	})
	if err != nil {
		return "", false, transient(err)
	}
	if inserted == 0 {
		stored, err := r.GetJobByTask(ctx, job.TaskID)
		if err != nil {
			return "", false, err
		}
		return stored.InputDigest, false, nil
	}
	return digest, true, nil
}

func (r *Repository) GetJobByTask(ctx context.Context, taskID string) (domain.Job, error) {
	row, err := r.queries.GetBuildJobByTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Job{}, domain.ErrNotFound
		}
		return domain.Job{}, transient(err)
	}
	return jobFromRow(row), nil
}

func (r *Repository) ListRunnable(ctx context.Context, limit int, now time.Time) ([]domain.Job, error) {
	rows, err := r.queries.ListRunnableBuildJobs(ctx, buildtestdb.ListRunnableBuildJobsParams{Now: &now, RowLimit: int32(limit)})
	if err != nil {
		return nil, transient(err)
	}
	jobs := make([]domain.Job, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, jobFromRow(row))
	}
	return jobs, nil
}

func (r *Repository) ClaimJob(ctx context.Context, jobID, leaseOwner string, leaseUntil, now time.Time) (bool, error) {
	claimed, err := r.queries.ClaimBuildJob(ctx, buildtestdb.ClaimBuildJobParams{
		LeaseOwner: text(leaseOwner), LeaseUntil: &leaseUntil, Now: now, ID: jobID,
	})
	if err != nil {
		return false, transient(err)
	}
	return claimed > 0, nil
}

func (r *Repository) RecordVerdict(ctx context.Context, jobID, leaseOwner string, verdict domain.Job) error {
	updated, err := r.queries.RecordBuildVerdict(ctx, buildtestdb.RecordBuildVerdictParams{
		State: string(verdict.State), Stage: string(verdict.Stage),
		BuildExitCode: int4(verdict.BuildExitCode), TestExitCode: int4(verdict.TestExitCode),
		FailureReason: failureText(verdict.FailureReason), EngineFacts: verdict.EngineFacts,
		LogTail: text(verdict.LogTail), ID: jobID, LeaseOwner: text(leaseOwner), Now: time.Now().UTC(),
	})
	if err != nil {
		return transient(err)
	}
	if updated == 0 {
		// The lease was taken over or the job was cancelled meanwhile; the
		// takeover's verdict is authoritative.
		return domain.ErrNotFound
	}
	return nil
}

func (r *Repository) RequeueTransient(ctx context.Context, jobID, leaseOwner string, attempts, maxAttempts int32, stage domain.Stage) error {
	if attempts < maxAttempts {
		updated, err := r.queries.RequeueBuildJob(ctx, buildtestdb.RequeueBuildJobParams{Stage: string(stage), ID: jobID, LeaseOwner: text(leaseOwner), Now: time.Now().UTC()})
		if err != nil {
			return transient(err)
		}
		if updated == 0 {
			return domain.ErrNotFound
		}
		return nil
	}
	updated, err := r.queries.FailBuildJob(ctx, buildtestdb.FailBuildJobParams{Stage: string(stage), ID: jobID, LeaseOwner: text(leaseOwner), Now: time.Now().UTC()})
	if err != nil {
		return transient(err)
	}
	if updated == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *Repository) CancelJob(ctx context.Context, taskID string, now time.Time) (domain.State, error) {
	updated, err := r.queries.CancelBuildJob(ctx, buildtestdb.CancelBuildJobParams{TaskID: taskID, Now: now})
	if err != nil {
		return "", transient(err)
	}
	if updated == 0 {
		job, err := r.GetJobByTask(ctx, taskID)
		if err != nil {
			return "", err
		}
		return job.State, nil
	}
	return domain.StateCancelled, nil
}

func failureText(reason domain.FailureReason) pgtype.Text {
	if reason == domain.FailureNone {
		return pgtype.Text{}
	}
	return pgtype.Text{String: string(reason), Valid: true}
}

func jobFromRow(row buildtestdb.WorkosRuntimeBuildJob) domain.Job {
	job := domain.Job{
		ID: row.ID, TaskID: row.TaskID, IncidentID: row.IncidentID,
		OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID, InstallationID: row.InstallationID,
		InputDigest: row.InputDigest, SourceBundleID: row.SourceBundleID, SourceDigest: row.SourceDigest,
		ManifestDigest: row.ManifestDigest, BaseImage: row.BaseImage,
		State: domain.State(row.State), Stage: domain.Stage(row.Stage),
		EngineFacts: row.EngineFacts, Attempts: row.Attempts, LogTail: row.LogTail.String,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.BuildExitCode.Valid {
		job.BuildExitCode = &row.BuildExitCode.Int32
	}
	if row.TestExitCode.Valid {
		job.TestExitCode = &row.TestExitCode.Int32
	}
	if row.FailureReason.Valid {
		job.FailureReason = domain.FailureReason(row.FailureReason.String)
	}
	if len(row.Payload) > 0 {
		_ = json.Unmarshal(row.Payload, &job.Payload)
	}
	return job
}
