// PostgreSQL rebuild facts: generation-scoped snapshot applies, durable job
// phases, and the single-row CAS promotion. Every statement touches only
// workos_index tables.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	indexerdb "github.com/yangtao121/workos/internal/indexer/adapters/postgres/indexerdb"
	indexerapp "github.com/yangtao121/workos/internal/indexer/application"
	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/platform/ids"
)

var (
	domainNotFound       = domain.ErrNotFound
	errCorruptProjection = domain.ErrCorrupt
)

type RebuildStore struct {
	pool    *pgxpool.Pool
	queries *indexerdb.Queries
	ids     ids.Generator
}

func NewRebuildStore(pool *pgxpool.Pool, generator ids.Generator) (*RebuildStore, error) {
	if pool == nil || generator == nil {
		return nil, errors.New("indexer rebuild store requires pool and id generator")
	}
	return &RebuildStore{pool: pool, queries: indexerdb.New(pool), ids: generator}, nil
}

// AdjudicateRebuildRequest decides the idempotency key in the database and
// creates the job + generation through the caller's create function when the
// key is fresh.
func (s *RebuildStore) AdjudicateRebuildRequest(ctx context.Context, key, digest string, create func(ctx context.Context) (indexerapp.RebuildJobView, error)) (indexerapp.RebuildJobView, bool, error) {
	stored, err := s.queries.GetRebuildJobRequest(ctx, key)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return indexerapp.RebuildJobView{}, false, storeError("read rebuild request", err)
	default:
		job, err := s.GetRebuildJob(ctx, stored.JobID)
		if err != nil {
			return indexerapp.RebuildJobView{}, false, err
		}
		if stored.RequestDigest != digest {
			return indexerapp.RebuildJobView{}, false, indexerapp.ErrRebuildConflict
		}
		return job, false, nil
	}
	job, err := create(ctx)
	if err != nil {
		return indexerapp.RebuildJobView{}, false, err
	}
	if err := s.queries.InsertRebuildJobRequest(ctx, indexerdb.InsertRebuildJobRequestParams{
		IdempotencyKey: key, RequestDigest: digest, JobID: job.ID, CreatedAt: job.CreatedAt,
	}); err != nil {
		return indexerapp.RebuildJobView{}, false, storeError("insert rebuild request", err)
	}
	return job, true, nil
}

func (s *RebuildStore) GetRebuildJob(ctx context.Context, jobID string) (indexerapp.RebuildJobView, error) {
	row, err := s.queries.GetRebuildJob(ctx, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return indexerapp.RebuildJobView{}, domainNotFound
	}
	if err != nil {
		return indexerapp.RebuildJobView{}, storeError("read rebuild job", err)
	}
	return rebuildJobFromRow(row), nil
}

func (s *RebuildStore) LiveRebuildJobs(ctx context.Context) ([]indexerapp.RebuildJobView, error) {
	rows, err := s.queries.GetLiveRebuildJobs(ctx)
	if err != nil {
		return nil, storeError("list live rebuild jobs", err)
	}
	jobs := make([]indexerapp.RebuildJobView, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, rebuildJobFromRow(row))
	}
	return jobs, nil
}

func (s *RebuildStore) SaveRebuildJob(ctx context.Context, job indexerapp.RebuildJobView) error {
	err := s.queries.UpdateRebuildJob(ctx, indexerdb.UpdateRebuildJobParams{
		ID: job.ID, State: job.State, PhaseCursor: job.PhaseCursor,
		SnapshotBoundary: job.SnapshotBoundary, SourceCount: job.SourceCount,
		AppliedCount: job.AppliedCount, TombstoneCount: job.TombstoneCount,
		FailureCategory: pgtype.Text{String: job.FailureCategory, Valid: job.FailureCategory != ""},
		UpdatedAt:       job.UpdatedAt,
		TerminalAt:      nullableTime(job.TerminalAt),
	})
	if err != nil {
		return storeError("save rebuild job", err)
	}
	return nil
}

func (s *RebuildStore) CancelRebuildJob(ctx context.Context, jobID string, now time.Time) (bool, error) {
	rows, err := s.queries.CancelRebuildJob(ctx, indexerdb.CancelRebuildJobParams{
		ID:              jobID,
		FailureCategory: pgtype.Text{String: "operator-canceled", Valid: true},
		UpdatedAt:       now,
	})
	if err != nil {
		return false, storeError("cancel rebuild job", err)
	}
	return rows == 1, nil
}

func (s *RebuildStore) CreateGeneration(ctx context.Context, id, scope, ownerUserID, projectID string, now time.Time) error {
	var ownerText, projectText pgtype.UUID
	if ownerUserID != "" {
		ownerText = pgtype.UUID{Bytes: mustUUIDBytesValue(ownerUserID), Valid: true}
	}
	if projectID != "" {
		projectText = pgtype.UUID{Bytes: mustUUIDBytesValue(projectID), Valid: true}
	}
	return storeError("insert rebuild generation", s.queries.InsertGenerationFull(ctx, indexerdb.InsertGenerationFullParams{
		ID: id, Scope: scope, OwnerUserID: ownerText, ProjectID: projectText,
		Status: "building", CreatedAt: now,
	}))
}

// CreateRebuildJob persists the requested job row.
func (s *RebuildStore) CreateRebuildJob(ctx context.Context, job indexerapp.RebuildJobView, requestDigest string) error {
	var ownerText, projectText pgtype.UUID
	if job.OwnerUserID != "" {
		ownerText = pgtype.UUID{Bytes: mustUUIDBytesValue(job.OwnerUserID), Valid: true}
	}
	if job.ProjectID != "" {
		projectText = pgtype.UUID{Bytes: mustUUIDBytesValue(job.ProjectID), Valid: true}
	}
	return storeError("insert rebuild job", s.queries.InsertRebuildJob(ctx, indexerdb.InsertRebuildJobParams{
		ID: job.ID, Scope: job.Scope, OwnerUserID: ownerText, ProjectID: projectText,
		IdempotencyDigest: requestDigest, TargetGeneration: job.TargetGeneration,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}))
}

func (s *RebuildStore) ApplySnapshotSource(ctx context.Context, effect indexerapp.SnapshotEffect, generation, requestDigest string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return storeError("begin snapshot apply", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := s.queries.WithTx(tx)
	if !effect.Tombstone {
		rows, err := queries.ApplyResolvedSourceToGeneration(ctx, indexerdb.ApplyResolvedSourceToGenerationParams{
			ProjectionGeneration: generation,
			OwnerUserID:          effect.OwnerUserID,
			ProjectID:            effect.ProjectID,
			SourceID:             effect.ArtifactID,
			SourceDigest:         effect.Digest,
			ArtifactType:         effect.ArtifactType,
			Title:                effect.Title,
			Content:              string(effect.Content),
			SourceCreatedAt:      effect.CreatedAt,
			LastPublicationID:    effect.PublicationID,
			IndexedAt:            now,
			UpdatedAt:            now,
		})
		if err != nil {
			return storeError("apply snapshot document", err)
		}
		if rows != 1 {
			return errCorruptProjection
		}
	} else {
		if _, err := queries.TombstoneGenerationDocuments(ctx, indexerdb.TombstoneGenerationDocumentsParams{
			ProjectionGeneration: generation,
			OwnerUserID:          effect.OwnerUserID,
			ProjectID:            effect.ProjectID,
			TombstonedAt:         &now,
			UpdatedAt:            now,
		}); err != nil {
			return storeError("tombstone generation documents", err)
		}
	}
	outcome := "applied"
	if effect.Tombstone {
		outcome = "tombstoned"
	}
	if err := queries.UpsertReceiptForGeneration(ctx, indexerdb.UpsertReceiptForGenerationParams{
		PublicationID:        effect.PublicationID,
		ProjectionGeneration: generation,
		RequestDigest:        requestDigest,
		Outcome:              outcome,
		SourceDigest:         pgtype.Text{String: effect.Digest, Valid: effect.Digest != ""},
		ProcessedAt:          now,
	}); err != nil {
		return storeError("record snapshot receipt", err)
	}
	return tx.Commit(ctx)
}

// ValidateGeneration compares the target generation's document set against
// the authoritative digest map inside one ordered database walk.
func (s *RebuildStore) ValidateGeneration(ctx context.Context, generation string, authoritative map[string]string) (bool, error) {
	counts, err := s.queries.CountGenerationDocs(ctx, generation)
	if err != nil {
		return false, storeError("count generation documents", err)
	}
	if counts.Documents != int64(len(authoritative)) {
		return false, nil
	}
	cursor := time.Time{}
	cursorID := uuid.Nil.String()
	seen := 0
	for {
		rows, err := s.queries.WalkGenerationDocumentsAfter(ctx, indexerdb.WalkGenerationDocumentsAfterParams{
			GenerationID:    generation,
			CursorCreatedAt: cursor,
			CursorSourceID:  cursorID,
			PageLimit:       200,
		})
		if err != nil {
			return false, storeError("walk generation documents", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if row.TombstonedAt != nil {
				return false, nil
			}
			if digest, known := authoritative[row.SourceID]; !known || digest != row.SourceDigest {
				return false, nil
			}
			cursorID = row.SourceID
			seen++
		}
		if len(rows) < 200 {
			break
		}
	}
	return seen == len(authoritative), nil
}

func (s *RebuildStore) PromoteCAS(ctx context.Context, target, expectCurrent string, now time.Time) (bool, error) {
	rows, err := s.queries.CasPromoteGeneration(ctx, indexerdb.CasPromoteGenerationParams{
		Target: target, ExpectCurrent: expectCurrent,
	})
	if err != nil {
		return false, storeError("promote generation", err)
	}
	return rows == 1, nil
}

func (s *RebuildStore) MarkGeneration(ctx context.Context, id, status string, now time.Time) error {
	var promoted, retired *time.Time
	switch status {
	case "active":
		promoted = &now
	case "retired", "failed", "canceled":
		retired = &now
	}
	return storeError("mark generation", s.queries.UpdateGenerationStatus(ctx, indexerdb.UpdateGenerationStatusParams{
		ID: id, Status: status, PromotedAt: promoted, RetiredAt: retired,
	}))
}

func rebuildJobFromRow(row indexerdb.WorkosIndexRebuildJob) indexerapp.RebuildJobView {
	return indexerapp.RebuildJobView{
		ID: row.ID, Scope: row.Scope,
		OwnerUserID: uuidOrEmpty(row.OwnerUserID), ProjectID: uuidOrEmpty(row.ProjectID),
		State: row.State, PhaseCursor: row.PhaseCursor, SnapshotBoundary: row.SnapshotBoundary,
		SourceCount: row.SourceCount, AppliedCount: row.AppliedCount, TombstoneCount: row.TombstoneCount,
		FailureCategory: row.FailureCategory.String, TargetGeneration: row.TargetGeneration,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		TerminalAt: terminalTime(row.TerminalAt),
	}
}

func uuidOrEmpty(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func terminalTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func mustUUIDBytesValue(value string) [16]byte {
	parsed, err := uuid.Parse(value)
	if err != nil {
		panic("indexer rebuild store received a non-UUID id: " + err.Error())
	}
	return [16]byte(parsed)
}
