// PostgreSQL projection adapter: the only door to the workos_index schema.
// Receipts, document effects, tombstones, and the consumer cursor commit
// inside one local transaction per consumed publication; search reads only
// the active generation through the deterministic lexical page. Every
// failure is classified at the port boundary.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	indexerdb "github.com/yangtao121/workos/internal/indexer/adapters/postgres/indexerdb"
	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
	dbtransient "github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *indexerdb.Queries
	ids     ids.Generator
}

func New(pool *pgxpool.Pool, generator ids.Generator) (*Repository, error) {
	if pool == nil || generator == nil {
		return nil, errors.New("indexer projection repository requires pool and id generator")
	}
	return &Repository{pool: pool, queries: indexerdb.New(pool), ids: generator}, nil
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

// ActiveGenerationID returns the generation every search reads.
func (r *Repository) ActiveGenerationID(ctx context.Context) (string, error) {
	id, err := r.queries.ActiveGenerationID(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", storeError("read active generation", err)
	}
	if !domain.ValidUUID(id) {
		return "", domain.ErrCorrupt
	}
	return id, nil
}

// WritableGenerationIDs returns the active generation plus every building
// generation that mirrors live effects for the given scope.
func (r *Repository) WritableGenerationIDs(ctx context.Context, ownerUserID, projectID string) ([]string, error) {
	rows, err := r.queries.WritableGenerationIDs(ctx, indexerdb.WritableGenerationIDsParams{
		OwnerUserID: ownerUserID, ProjectID: projectID,
	})
	if err != nil {
		return nil, storeError("read writable generations", err)
	}
	ids := make([]string, 0, len(rows))
	for _, id := range rows {
		if !domain.ValidUUID(id) {
			return nil, domain.ErrCorrupt
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (r *Repository) EnsureBootstrapGeneration(ctx context.Context, now time.Time) (string, error) {
	id, err := r.ActiveGenerationID(ctx)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return "", err
	}
	generation := r.ids.New()
	if err := r.queries.InsertGeneration(ctx, indexerdb.InsertGenerationParams{
		ID: generation, Scope: "all", Status: "active", CreatedAt: canonical(now),
	}); err != nil {
		return "", storeError("insert bootstrap generation", err)
	}
	rows, err := r.queries.ActivateGenerationIfEmpty(ctx, generation)
	if err != nil {
		return "", storeError("activate bootstrap generation", err)
	}
	if rows == 0 {
		// A concurrent boot won the activation: read its generation.
		return r.ActiveGenerationID(ctx)
	}
	return generation, nil
}

// ApplyResolvedSource projects one resolved source: the document or
// tombstone effect plus receipts across every writable generation plus the
// consumer cursor, in one local transaction. The receipt is the physical
// exactly-once arbiter per generation; same publication + same digest
// replays as a no-op, and the same publication with a drifted digest is
// corruption instead of an overwrite.
func (r *Repository) ApplyResolvedSource(ctx context.Context, source ports.ResolvedSource, outcome, requestDigest string, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return storeError("begin apply resolved source", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	writable, err := queries.WritableGenerationIDs(ctx, indexerdb.WritableGenerationIDsParams{
		OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID,
	})
	if err != nil {
		return storeError("read writable generations", err)
	}

	// Tombstone arbitration: once a project tombstone is recorded, a late or
	// replayed upsert is recorded as a tombstoned receipt, never a document.
	var tombstoned bool
	if source.Operation == tombstoneOperation {
		if err := queries.UpsertProjectTombstone(ctx, indexerdb.UpsertProjectTombstoneParams{
			OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID,
			LastPublicationID: source.PublicationID, ArchivedAt: canonical(source.OccurredAt),
		}); err != nil {
			return storeError("record project tombstone", err)
		}
		tombstoned = true
	} else {
		row, rowErr := queries.GetProjectTombstone(ctx, indexerdb.GetProjectTombstoneParams{
			OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID,
		})
		switch {
		case errors.Is(rowErr, pgx.ErrNoRows):
		case rowErr != nil:
			return storeError("read project tombstone", rowErr)
		default:
			// Tombstone wins over every earlier upsert.
			if !row.ArchivedAt.Before(canonical(source.OccurredAt)) {
				tombstoned = true
			}
		}
	}

	if tombstoned && outcome == domain.OutcomeApplied {
		outcome = domain.OutcomeTombstoned
	}

	for _, generation := range writable {
		receipt, err := queries.GetReceipt(ctx, indexerdb.GetReceiptParams{
			PublicationID: source.PublicationID, ProjectionGeneration: generation,
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// First delivery for this generation.
		case err != nil:
			return storeError("read receipt", err)
		default:
			if receipt.RequestDigest != requestDigest {
				// Same publication with a different canonical effect is
				// corruption: the receipt stands and the write never happens.
				return domain.ErrCorrupt
			}
			// Exact replay: the effect already happened; the receipt stands.
			continue
		}

		if !tombstoned && outcome == domain.OutcomeApplied {
			rows, err := queries.UpsertSearchDocument(ctx, indexerdb.UpsertSearchDocumentParams{
				ProjectionGeneration: generation,
				OwnerUserID:          source.OwnerUserID,
				ProjectID:            source.ProjectID,
				SourceID:             source.ArtifactID,
				SourceDigest:         source.Digest,
				ArtifactType:         source.ArtifactType,
				Title:                source.Title,
				Content:              string(source.Content),
				SourceCreatedAt:      canonical(source.CreatedAt),
				LastPublicationID:    source.PublicationID,
				IndexedAt:            canonical(now),
				UpdatedAt:            canonical(now),
			})
			if err != nil {
				return storeError("upsert search document", err)
			}
			if rows != 1 {
				return domain.ErrCorrupt
			}
		}
		if tombstoned {
			if _, err := queries.TombstoneProjectDocuments(ctx, indexerdb.TombstoneProjectDocumentsParams{
				ProjectionGeneration: generation,
				OwnerUserID:          source.OwnerUserID,
				ProjectID:            source.ProjectID,
				TombstonedAt:         timePtr(canonical(source.OccurredAt)),
				UpdatedAt:            canonical(now),
			}); err != nil {
				return storeError("tombstone documents", err)
			}
		}
		if err := queries.UpsertReceipt(ctx, indexerdb.UpsertReceiptParams{
			PublicationID:        source.PublicationID,
			ProjectionGeneration: generation,
			RequestDigest:        requestDigest,
			Outcome:              outcome,
			SourceDigest:         pgtype.Text{String: source.Digest, Valid: source.Digest != ""},
			ProcessedAt:          canonical(now),
		}); err != nil {
			return storeError("record receipt", err)
		}
	}
	if err := queries.UpsertConsumerCursor(ctx, indexerdb.UpsertConsumerCursorParams{
		WorkerID:            cursorWorkerID,
		CursorPublicationID: pgtype.UUID{Bytes: mustUUIDBytes(source.PublicationID), Valid: true},
		CursorOccurredAt:    timePtr(canonical(source.OccurredAt)),
		UpdatedAt:           canonical(now),
	}); err != nil {
		return storeError("advance consumer cursor", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return storeError("commit apply resolved source", err)
	}
	return nil
}

// cursorWorkerID names the durable consumer cursor row.
const cursorWorkerID = "projection"

const tombstoneOperation = "project.tombstone"

// Search runs one bounded deterministic lexical page over the active
// generation and revalidates every stored fact it returns.
func (r *Repository) Search(ctx context.Context, query domain.SearchQuery) (domain.SearchPage, error) {
	generation, err := r.ActiveGenerationID(ctx)
	if err != nil {
		return domain.SearchPage{}, err
	}
	snapshot := canonical(time.Now().UTC())
	cursorScore := 1e9
	var cursorCreated time.Time
	var cursorSource string
	if query.Decoded != nil {
		if query.Decoded.GenerationID != generation {
			// The generation moved (rebuild promoted): the old chain must not
			// mix documents across generations.
			return domain.SearchPage{}, domain.ErrInvalid
		}
		snapshot = canonical(query.Decoded.SnapshotThrough)
		cursorScore = query.Decoded.LastScore
		cursorCreated = canonical(query.Decoded.LastSourceCreated)
		cursorSource = query.Decoded.LastSourceID
	}
	rows, err := r.queries.SearchProjectDocuments(ctx, indexerdb.SearchProjectDocumentsParams{
		GenerationID:    generation,
		OwnerUserID:     query.OwnerUserID,
		ProjectID:       query.ProjectID,
		QueryText:       query.CanonicalQuery,
		SnapshotThrough: snapshot,
		CursorScore:     cursorScore,
		CursorCreatedAt: cursorCreated,
		CursorSourceID:  cursorSource,
		RowLimit:        int32(query.PageSize + 1),
	})
	if err != nil {
		return domain.SearchPage{}, storeError("search documents", err)
	}
	more := len(rows) > query.PageSize
	if more {
		rows = rows[:query.PageSize]
	}
	page := domain.SearchPage{GenerationID: generation, SnapshotThrough: snapshot}
	for _, row := range rows {
		if domain.ValidStoredScore(float64(row.Score)) != nil {
			return domain.SearchPage{}, domain.ErrCorrupt
		}
		if !domain.ValidDigest(row.SourceDigest) || !domain.ValidUUID(row.SourceID) {
			return domain.SearchPage{}, domain.ErrCorrupt
		}
		page.Hits = append(page.Hits, domain.SearchHit{
			ContextRef:   domain.ContextRefString(row.SourceID, row.SourceDigest),
			Excerpt:      domain.BuildExcerpt(domain.ExcerptRequest{Content: row.Content, Terms: queryTerms(query.CanonicalQuery)}),
			Score:        float64(row.Score),
			ArtifactID:   row.SourceID,
			ArtifactType: row.ArtifactType,
			Digest:       row.SourceDigest,
			Title:        row.Title,
			CreatedAt:    row.SourceCreatedAt,
		})
	}
	if more {
		last := rows[len(rows)-1]
		token, err := domain.EncodePageToken(domain.PageToken{
			OwnerUserID: query.OwnerUserID, ProjectID: query.ProjectID,
			QueryDigest: query.QueryDigest, RankingVersion: domain.RankingVersion,
			GenerationID: generation, SnapshotThrough: snapshot,
			LastScore: float64(last.Score), LastSourceCreated: last.SourceCreatedAt, LastSourceID: last.SourceID,
		})
		if err != nil {
			return domain.SearchPage{}, err
		}
		page.NextPageToken = token
	}
	return page, nil
}

// Freshness reads the bounded freshness projection from durable facts: the
// consumed publication watermark (cursor), the newest indexed document, and
// the Core-side pending count.
func (r *Repository) Freshness(ctx context.Context, pending int64) (domain.Freshness, error) {
	lastIndexed, err := r.queries.SearchFreshness(ctx)
	if err != nil {
		return domain.Freshness{}, storeError("read freshness", err)
	}
	freshness := domain.Freshness{
		CaughtUp:            pending == 0,
		LastIndexedAt:       lastIndexed,
		IndexedThrough:      time.Time{},
		PendingPublications: pending,
	}
	if cursor, err := r.queries.GetConsumerCursor(ctx, cursorWorkerID); err == nil {
		if cursor.CursorOccurredAt != nil {
			freshness.IndexedThrough = *cursor.CursorOccurredAt
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Freshness{}, storeError("read consumer cursor", err)
	}
	return freshness, nil
}

func queryTerms(canonical string) []string {
	return strings.Fields(strings.ToLower(canonical))
}

func timePtr(value time.Time) *time.Time { return &value }

func mustUUIDBytes(value string) [16]byte {
	parsed, err := uuid.Parse(value)
	if err != nil {
		panic("indexer adapter received a non-UUID id: " + err.Error())
	}
	return [16]byte(parsed)
}

// DocumentStatus reports the active-generation state of one source.
func (r *Repository) DocumentStatus(ctx context.Context, ownerUserID, projectID, sourceID string) (ports.DocumentStatus, error) {
	row, err := r.queries.GetDocumentStatus(ctx, indexerdb.GetDocumentStatusParams{
		OwnerUserID: ownerUserID, ProjectID: projectID, SourceID: sourceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.DocumentStatus{}, nil
	}
	if err != nil {
		return ports.DocumentStatus{}, storeError("read document status", err)
	}
	return ports.DocumentStatus{Known: true, Digest: row.SourceDigest, Tombstoned: row.TombstonedAt != nil}, nil
}
