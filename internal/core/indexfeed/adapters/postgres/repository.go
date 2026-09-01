// PostgreSQL adapter for the Core index publication feed. The sink appends
// facts inside the caller's source-mutation transaction; the store arbitrates
// claims in the database (FOR UPDATE SKIP LOCKED + live-lease predicates), so
// two workers can never hold the same lease and a stale complete can never
// satisfy a new one. Every failure is classified at the port boundary; the
// classification never reads SQLSTATE message text or constraint names.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	indexfeeddb "github.com/yangtao121/workos/internal/core/indexfeed/adapters/postgres/indexfeeddb"
	"github.com/yangtao121/workos/internal/core/indexfeed/domain"
	"github.com/yangtao121/workos/internal/core/indexfeed/ports"
	"github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// Repository implements both the tx-scoped sink and the claim store.
type Repository struct {
	pool    *pgxpool.Pool
	queries *indexfeeddb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: indexfeeddb.New(pool)}
}

func storeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", operation, ports.ErrStoreUnavailable, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func canonical(value time.Time) time.Time {
	return domain.CanonicalUTCTime(value)
}

// timePtr hands the database a canonical UTC microsecond instant; the
// generated nullable timestamptz params are *time.Time.
func timePtr(value time.Time) *time.Time {
	canonical := domain.CanonicalUTCTime(value)
	return &canonical
}

func textValue(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func textPtr(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// AppendReviewArtifactUpsert appends the upsert publication inside the
// caller's transaction. Zero rows means the per-artifact unique arbitration
// was violated: the artifact is immutable and inserted once, so a second
// publication is stored corruption and must roll the source mutation back.
func (r *Repository) AppendReviewArtifactUpsert(ctx context.Context, tx dbtx.Tx, publication domain.Publication) error {
	rows, err := r.queries.WithTx(tx).AppendIndexPublication(ctx, indexfeeddb.AppendIndexPublicationParams{
		ID: publication.ID, Operation: domain.OperationReviewArtifactUpsert,
		OwnerUserID: publication.OwnerUserID, ProjectID: publication.ProjectID,
		SourceType:   domain.SourceType,
		SourceID:     pgtype.UUID{Bytes: mustUUIDBytes(publication.SourceID), Valid: true},
		ArtifactType: textValue(publication.ArtifactType),
		Digest:       textValue(publication.Digest),
		OccurredAt:   canonical(publication.OccurredAt), CreatedAt: canonical(publication.OccurredAt),
	})
	if err != nil {
		return storeError("append review artifact upsert publication", err)
	}
	if rows == 0 {
		return domain.ErrCorrupt
	}
	return nil
}

// AppendProjectTombstone appends the tombstone publication inside the
// caller's archive transaction. Zero rows means the per-project unique
// arbitration was violated: archive is a guarded one-time transition, so a
// second tombstone is stored corruption.
func (r *Repository) AppendProjectTombstone(ctx context.Context, tx dbtx.Tx, publication domain.Publication) error {
	rows, err := r.queries.WithTx(tx).AppendIndexPublication(ctx, indexfeeddb.AppendIndexPublicationParams{
		ID: publication.ID, Operation: domain.OperationProjectTombstone,
		OwnerUserID: publication.OwnerUserID, ProjectID: publication.ProjectID,
		SourceType:   domain.SourceType,
		SourceID:     pgtype.UUID{},
		ArtifactType: pgtype.Text{},
		Digest:       pgtype.Text{},
		OccurredAt:   canonical(publication.OccurredAt), CreatedAt: canonical(publication.OccurredAt),
	})
	if err != nil {
		return storeError("append project tombstone publication", err)
	}
	if rows == 0 {
		return domain.ErrCorrupt
	}
	return nil
}

// ClaimPending leases pending publications to this worker. The database
// arbitrates the lease: the subquery locks rows FOR UPDATE, so two
// concurrent claims of the same publication cannot both succeed.
func (r *Repository) ClaimPending(ctx context.Context, workerID, leaseToken string, leaseUntil, now time.Time, maxBatch int) ([]ports.ClaimedPublication, error) {
	rows, err := r.queries.ClaimPendingIndexPublications(ctx, indexfeeddb.ClaimPendingIndexPublicationsParams{
		WorkerID: textValue(workerID), ClaimToken: textValue(leaseToken),
		LeaseUntil: timePtr(leaseUntil), Now: timePtr(now),
		MaxBatch: int32(maxBatch),
	})
	if err != nil {
		return nil, storeError("claim pending index publications", err)
	}
	claimed := make([]ports.ClaimedPublication, 0, len(rows))
	for _, row := range rows {
		publication := domain.Publication{
			ID: row.ID, Operation: row.Operation,
			OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
			SourceType:   row.SourceType,
			SourceID:     uuidString(row.SourceID),
			ArtifactType: textPtr(row.ArtifactType),
			Digest:       textPtr(row.Digest),
			OccurredAt:   row.OccurredAt,
		}
		if err := domain.ValidStoredPublication(publication); err != nil {
			return nil, err
		}
		claimed = append(claimed, ports.ClaimedPublication{
			Publication: publication, LeaseToken: leaseToken, ExpiresAt: leaseUntil,
		})
	}
	return claimed, nil
}

// LockForResolve locks one live claim inside the caller's transaction.
func (r *Repository) LockForResolve(ctx context.Context, tx dbtx.Tx, publicationID, workerID, leaseToken string, now time.Time) (domain.Publication, error) {
	row, err := r.queries.WithTx(tx).LockIndexPublicationForResolve(ctx, indexfeeddb.LockIndexPublicationForResolveParams{
		ID: publicationID, WorkerID: textValue(workerID), ClaimToken: textValue(leaseToken), Now: timePtr(now),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Publication{}, domain.ErrLeaseStale
	}
	if err != nil {
		return domain.Publication{}, storeError("lock index publication for resolve", err)
	}
	publication := domain.Publication{
		ID: row.ID, Operation: row.Operation,
		OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
		SourceType:   row.SourceType,
		SourceID:     uuidString(row.SourceID),
		ArtifactType: textPtr(row.ArtifactType),
		Digest:       textPtr(row.Digest),
		OccurredAt:   row.OccurredAt,
	}
	if err := domain.ValidStoredPublication(publication); err != nil {
		return domain.Publication{}, err
	}
	return publication, nil
}

// Complete records terminal outcomes inside the caller's transaction and
// reports which claims this worker actually acked; stale claims stay false
// so the consumer replays them against its durable receipt.
func (r *Repository) Complete(ctx context.Context, tx dbtx.Tx, workerID string, results []ports.CompleteResult, now time.Time) (map[string]bool, error) {
	acked := make(map[string]bool, len(results))
	for _, result := range results {
		rows, err := r.queries.WithTx(tx).CompleteIndexPublication(ctx, indexfeeddb.CompleteIndexPublicationParams{
			Outcome: textValue(result.Outcome), Now: timePtr(now), WorkerID: textValue(workerID),
			ID: result.PublicationID, ClaimToken: textValue(result.LeaseToken),
		})
		if err != nil {
			return nil, storeError("complete index publication", err)
		}
		acked[result.PublicationID] = rows == 1
	}
	return acked, nil
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func mustUUIDBytes(value string) [16]byte {
	parsed, err := uuid.Parse(value)
	if err != nil {
		panic("indexfeed adapter received a non-UUID id: " + err.Error())
	}
	return [16]byte(parsed)
}
