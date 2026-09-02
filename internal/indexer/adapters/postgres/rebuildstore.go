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
	"github.com/jackc/pgx/v5/pgconn"
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

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isConstraint(err error, name string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == name
}

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

// AdjudicateRebuildRequest serializes one idempotency key and commits the
// generation, job, and request mapping together. There is no state in which
// a failed first response consumes the key or leaves an orphan generation.
func (s *RebuildStore) AdjudicateRebuildRequest(ctx context.Context, key, digest string, job indexerapp.RebuildJobView) (indexerapp.RebuildJobView, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return indexerapp.RebuildJobView{}, false, storeError("begin rebuild request", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := s.queries.WithTx(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1776987972))`, key); err != nil {
		return indexerapp.RebuildJobView{}, false, storeError("lock rebuild request", err)
	}
	stored, err := queries.GetRebuildJobRequest(ctx, key)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return indexerapp.RebuildJobView{}, false, storeError("read rebuild request", err)
	default:
		row, err := queries.GetRebuildJob(ctx, stored.JobID)
		if err != nil {
			return indexerapp.RebuildJobView{}, false, storeError("read rebuild job", err)
		}
		if stored.RequestDigest != digest {
			return indexerapp.RebuildJobView{}, false, indexerapp.ErrRebuildConflict
		}
		replayed := rebuildJobFromRow(row)
		if err := indexerapp.ValidateStoredRebuildJob(replayed); err != nil {
			return indexerapp.RebuildJobView{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return indexerapp.RebuildJobView{}, false, storeError("commit rebuild replay", err)
		}
		return replayed, false, nil
	}
	var ownerID, projectID pgtype.UUID
	if job.OwnerUserID != "" {
		parsed, err := uuid.Parse(job.OwnerUserID)
		if err != nil {
			return indexerapp.RebuildJobView{}, false, domain.ErrInvalid
		}
		ownerID = pgtype.UUID{Bytes: parsed, Valid: true}
	}
	if job.ProjectID != "" {
		parsed, err := uuid.Parse(job.ProjectID)
		if err != nil {
			return indexerapp.RebuildJobView{}, false, domain.ErrInvalid
		}
		projectID = pgtype.UUID{Bytes: parsed, Valid: true}
	}
	if err := queries.InsertGenerationFull(ctx, indexerdb.InsertGenerationFullParams{
		ID: job.TargetGeneration, Scope: job.Scope, OwnerUserID: ownerID,
		ProjectID: projectID, Status: "building", CreatedAt: job.CreatedAt,
	}); err != nil {
		if isConstraint(err, "rebuild_jobs_single_live_unique") || isConstraint(err, "projection_generations_single_building_unique") {
			return indexerapp.RebuildJobView{}, false, indexerapp.ErrRebuildLiveScope
		}
		return indexerapp.RebuildJobView{}, false, storeError("insert rebuild generation", err)
	}
	if err := queries.InsertRebuildJob(ctx, indexerdb.InsertRebuildJobParams{
		ID: job.ID, Scope: job.Scope, OwnerUserID: ownerID, ProjectID: projectID,
		IdempotencyDigest: digest, TargetGeneration: job.TargetGeneration,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}); err != nil {
		if isUniqueViolation(err) {
			return indexerapp.RebuildJobView{}, false, indexerapp.ErrRebuildLiveScope
		}
		return indexerapp.RebuildJobView{}, false, storeError("insert rebuild job", err)
	}
	if err := queries.InsertRebuildJobRequest(ctx, indexerdb.InsertRebuildJobRequestParams{
		IdempotencyKey: key, RequestDigest: digest, JobID: job.ID, CreatedAt: job.CreatedAt,
	}); err != nil {
		return indexerapp.RebuildJobView{}, false, storeError("insert rebuild request", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return indexerapp.RebuildJobView{}, false, storeError("commit rebuild request", err)
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
	job := rebuildJobFromRow(row)
	if err := indexerapp.ValidateStoredRebuildJob(job); err != nil {
		return indexerapp.RebuildJobView{}, err
	}
	return job, nil
}

func (s *RebuildStore) LiveRebuildJobs(ctx context.Context) ([]indexerapp.RebuildJobView, error) {
	rows, err := s.queries.GetLiveRebuildJobs(ctx)
	if err != nil {
		return nil, storeError("list live rebuild jobs", err)
	}
	jobs := make([]indexerapp.RebuildJobView, 0, len(rows))
	for _, row := range rows {
		job := rebuildJobFromRow(row)
		if err := indexerapp.ValidateStoredRebuildJob(job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, storeError("begin rebuild cancel", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var target string
	err = tx.QueryRow(ctx, `
		UPDATE workos_index.rebuild_jobs
		SET state = 'canceled', failure_category = 'operator-canceled',
		    updated_at = $2, terminal_at = $2
		WHERE id = $1 AND state IN ('requested','snapshotting','catching_up','validating')
		RETURNING target_generation::text`, jobID, now).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storeError("cancel rebuild job", err)
	}
	command, err := tx.Exec(ctx, `
		UPDATE workos_index.projection_generations
		SET status = 'canceled', retired_at = $2
		WHERE id = $1::uuid AND status = 'building'`, target, now)
	if err != nil {
		return false, storeError("cancel rebuild generation", err)
	}
	if command.RowsAffected() != 1 {
		return false, errCorruptProjection
	}
	if err := tx.Commit(ctx); err != nil {
		return false, storeError("commit rebuild cancel", err)
	}
	return true, nil
}

func (s *RebuildStore) ApplySnapshotSource(ctx context.Context, effect indexerapp.SnapshotEffect, generation, requestDigest string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return storeError("begin snapshot apply", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := s.queries.WithTx(tx)
	receipt, err := queries.GetReceipt(ctx, indexerdb.GetReceiptParams{
		PublicationID: effect.PublicationID, ProjectionGeneration: generation,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return storeError("read snapshot receipt", err)
	default:
		if receipt.RequestDigest != requestDigest {
			return errCorruptProjection
		}
		return tx.Commit(ctx)
	}
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
			cursor = row.SourceCreatedAt
			cursorID = row.SourceID
			if row.TombstonedAt != nil {
				if _, stillActive := authoritative[row.SourceID]; stillActive {
					return false, nil
				}
				continue
			}
			if digest, known := authoritative[row.SourceID]; !known || digest != row.SourceDigest {
				return false, nil
			}
			seen++
		}
		if len(rows) < 200 {
			break
		}
	}
	return seen == len(authoritative), nil
}

func (s *RebuildStore) CompletePromotion(ctx context.Context, jobID, target, expectCurrent string, now time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, storeError("begin generation promotion", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var state, storedTarget string
	if err := tx.QueryRow(ctx, `SELECT state, target_generation::text FROM workos_index.rebuild_jobs WHERE id = $1 FOR UPDATE`, jobID).Scan(&state, &storedTarget); err != nil {
		return false, storeError("lock rebuild job for promotion", err)
	}
	if storedTarget != target {
		return false, errCorruptProjection
	}
	var current string
	if err := tx.QueryRow(ctx, `SELECT generation_id::text FROM workos_index.active_generation FOR UPDATE`).Scan(&current); err != nil {
		return false, storeError("lock active generation", err)
	}
	if state == "completed" {
		if current != target {
			return false, errCorruptProjection
		}
		if err := tx.Commit(ctx); err != nil {
			return false, storeError("commit promotion replay", err)
		}
		return true, nil
	}
	if state != "promoting" {
		return false, errCorruptProjection
	}
	if current != expectCurrent {
		return false, nil
	}
	if current != target {
		if _, err := tx.Exec(ctx, `UPDATE workos_index.projection_generations SET status = 'retired', retired_at = $2 WHERE id = $1::uuid AND status = 'active'`, current, now); err != nil {
			return false, storeError("retire active generation", err)
		}
	}
	command, err := tx.Exec(ctx, `UPDATE workos_index.projection_generations SET status = 'active', promoted_at = $2, retired_at = NULL WHERE id = $1::uuid AND status IN ('building','active')`, target, now)
	if err != nil {
		return false, storeError("activate target generation", err)
	}
	if command.RowsAffected() != 1 {
		return false, errCorruptProjection
	}
	if _, err := tx.Exec(ctx, `UPDATE workos_index.active_generation SET generation_id = $1::uuid`, target); err != nil {
		return false, storeError("swap active generation", err)
	}
	command, err = tx.Exec(ctx, `UPDATE workos_index.rebuild_jobs SET state = 'completed', updated_at = $2, terminal_at = $2 WHERE id = $1 AND state = 'promoting'`, jobID, now)
	if err != nil {
		return false, storeError("complete rebuild job", err)
	}
	if command.RowsAffected() != 1 {
		return false, errCorruptProjection
	}
	if err := tx.Commit(ctx); err != nil {
		return false, storeError("commit generation promotion", err)
	}
	return true, nil
}

func (s *RebuildStore) FailRebuildJob(ctx context.Context, job indexerapp.RebuildJobView, category string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return storeError("begin rebuild failure", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	command, err := tx.Exec(ctx, `UPDATE workos_index.projection_generations SET status = 'failed', retired_at = $2 WHERE id = $1::uuid AND status = 'building'`, job.TargetGeneration, now)
	if err != nil {
		return storeError("fail rebuild generation", err)
	}
	if command.RowsAffected() != 1 {
		return errCorruptProjection
	}
	command, err = tx.Exec(ctx, `
		UPDATE workos_index.rebuild_jobs SET state = 'failed', failure_category = $2,
		updated_at = $3, terminal_at = $3
		WHERE id = $1 AND state IN ('requested','snapshotting','catching_up','validating','promoting')`, job.ID, category, now)
	if err != nil {
		return storeError("fail rebuild job", err)
	}
	if command.RowsAffected() != 1 {
		return errCorruptProjection
	}
	return tx.Commit(ctx)
}

func (s *RebuildStore) CleanupRetiredGeneration(ctx context.Context) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, storeError("begin retired generation cleanup", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var generation string
	err = tx.QueryRow(ctx, `
		SELECT g.id::text
		FROM workos_index.projection_generations g
		WHERE g.status = 'retired' AND (
			EXISTS (SELECT 1 FROM workos_index.documents d WHERE d.projection_generation = g.id)
			OR EXISTS (SELECT 1 FROM workos_index.publication_receipts r WHERE r.projection_generation = g.id)
		)
		ORDER BY g.retired_at, g.id
		FOR UPDATE OF g SKIP LOCKED
		LIMIT 1`).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storeError("select retired generation cleanup", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM workos_index.publication_receipts WHERE projection_generation = $1::uuid`, generation); err != nil {
		return false, storeError("delete retired generation receipts", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM workos_index.documents WHERE projection_generation = $1::uuid`, generation); err != nil {
		return false, storeError("delete retired generation documents", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, storeError("commit retired generation cleanup", err)
	}
	return true, nil
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
