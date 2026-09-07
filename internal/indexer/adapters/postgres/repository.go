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
	"math"
	"sort"
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

// ActiveGenerationStatus returns safe, revalidated operational facts for
// the single generation all searches currently read.
func (r *Repository) ActiveGenerationStatus(ctx context.Context) (domain.GenerationStatus, error) {
	id, err := r.ActiveGenerationID(ctx)
	if err != nil {
		return domain.GenerationStatus{}, err
	}
	row, err := r.queries.GetGeneration(ctx, id)
	if err != nil {
		return domain.GenerationStatus{}, storeError("read active generation status", err)
	}
	counts, err := r.queries.CountGenerationDocs(ctx, id)
	if err != nil {
		return domain.GenerationStatus{}, storeError("count active generation status", err)
	}
	status := domain.GenerationStatus{
		ID: id, Scope: row.Scope, Status: row.Status,
		DocumentCount: counts.Documents, TombstoneCount: counts.Tombstoned,
		CreatedAt: row.CreatedAt, PromotedAt: timeOrZero(row.PromotedAt),
	}
	if status.Status != "active" {
		return domain.GenerationStatus{}, domain.ErrCorrupt
	}
	if status.Status == "active" && status.PromotedAt.IsZero() {
		status.PromotedAt = status.CreatedAt
	}
	if err := domain.ValidGenerationStatus(status); err != nil {
		return domain.GenerationStatus{}, err
	}
	return status, nil
}

func timeOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// WritableGenerationIDs returns the active generation plus every building
// generation that mirrors live effects for the given scope.
func (r *Repository) WritableGenerationIDs(ctx context.Context, ownerUserID, projectID string) ([]string, error) {
	if !domain.ValidUUID(ownerUserID) || !domain.ValidUUID(projectID) {
		return nil, domain.ErrInvalid
	}
	rows, err := r.queries.WritableGenerationIDs(ctx)
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
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", storeError("begin bootstrap generation", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1776987973)`); err != nil {
		return "", storeError("lock bootstrap generation", err)
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT generation_id::text FROM workos_index.active_generation`).Scan(&existing)
	if err == nil {
		if !domain.ValidUUID(existing) {
			return "", domain.ErrCorrupt
		}
		if err := tx.Commit(ctx); err != nil {
			return "", storeError("commit bootstrap replay", err)
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", storeError("read bootstrap generation", err)
	}
	generation := r.ids.New()
	queries := r.queries.WithTx(tx)
	if err := queries.InsertGeneration(ctx, indexerdb.InsertGenerationParams{
		ID: generation, Scope: "all", Status: "active", CreatedAt: canonical(now),
	}); err != nil {
		return "", storeError("insert bootstrap generation", err)
	}
	rows, err := queries.ActivateGenerationIfEmpty(ctx, generation)
	if err != nil {
		return "", storeError("activate bootstrap generation", err)
	}
	if rows != 1 {
		return "", domain.ErrCorrupt
	}
	if err := tx.Commit(ctx); err != nil {
		return "", storeError("commit bootstrap generation", err)
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
	defer tx.Rollback(ctx)
	queries := r.queries.WithTx(tx)
	if _, err := queries.LockActiveGeneration(ctx); err != nil {
		return storeError("lock active generation", err)
	}
	if err := queries.LockIndexProject(ctx, source.OwnerUserID+"/"+source.ProjectID); err != nil {
		return storeError("lock index project", err)
	}
	if err := applyResolvedSource(ctx, queries, source, outcome, requestDigest, now); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return storeError("commit apply resolved source", err)
	}
	return nil
}

func applyResolvedSource(ctx context.Context, queries *indexerdb.Queries, source ports.ResolvedSource, outcome, requestDigest string, now time.Time) error {
	publicationUUID, err := uuid.Parse(source.PublicationID)
	if err != nil || publicationUUID.Version() != 7 || !domain.ValidUUID(source.PublicationID) ||
		!domain.ValidUUID(source.OwnerUserID) || !domain.ValidUUID(source.ProjectID) ||
		!domain.ValidDigest(requestDigest) || source.OccurredAt.IsZero() {
		return domain.ErrInvalid
	}
	var sourceType string
	switch source.Operation {
	case "review-artifact.upsert":
		sourceType = domain.SourceReviewArtifact
	case "workspace.upsert":
		sourceType = domain.SourceWorkspaceFile
	default:
		sourceType = ""
	}
	switch outcome {
	case domain.OutcomeApplied:
		document := domain.Document{
			OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID,
			SourceID: source.ArtifactID, SourceDigest: source.Digest,
			ArtifactType: source.ArtifactType, Title: source.Title,
			Content: string(source.Content), SourceCreatedAt: source.CreatedAt,
			LastPublication: source.PublicationID, IndexedAt: now,
		}
		if sourceType == "" || domain.ValidStoredDocument(document) != nil {
			return domain.ErrInvalid
		}
	case domain.OutcomeTombstoned:
		if source.Operation != tombstoneOperation && source.Operation != "review-artifact.upsert" {
			return domain.ErrInvalid
		}
	case domain.OutcomeUnsupported, domain.OutcomeCorrupt:
		if source.Operation != "review-artifact.upsert" {
			return domain.ErrInvalid
		}
	default:
		return domain.ErrInvalid
	}

	writable, err := queries.WritableGenerationIDs(ctx)
	if err != nil {
		return storeError("read writable generations", err)
	}
	if len(writable) == 0 {
		return domain.ErrCorrupt
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
		_, rowErr := queries.GetProjectTombstone(ctx, indexerdb.GetProjectTombstoneParams{
			OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID,
		})
		switch {
		case errors.Is(rowErr, pgx.ErrNoRows):
		case rowErr != nil:
			return storeError("read project tombstone", rowErr)
		default:
			// Project archival is terminal. Once its durable tombstone exists,
			// no replayed or out-of-order upsert may resurrect the scope.
			tombstoned = true
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
			// Deterministic local feature-hash embedding of the bounded
			// title+content (ADR-0017 §3): computed at write time so every
			// stored document carries its semantic projection.
			vector := domain.Embed(source.Title + "\n" + string(source.Content))
			embedding := make([]float32, len(vector))
			for i := range vector {
				embedding[i] = vector[i]
			}
			rows, err := queries.UpsertSearchDocument(ctx, indexerdb.UpsertSearchDocumentParams{
				ProjectionGeneration: generation,
				OwnerUserID:          source.OwnerUserID,
				ProjectID:            source.ProjectID,
				SourceType:           sourceType,
				SourceOperation:      source.Operation,
				SourceID:             source.ArtifactID,
				SourceDigest:         source.Digest,
				ArtifactType:         source.ArtifactType,
				Title:                source.Title,
				Content:              string(source.Content),
				SourceCreatedAt:      canonical(source.CreatedAt),
				LastPublicationID:    source.PublicationID,
				IndexedAt:            canonical(now),
				UpdatedAt:            canonical(now),
				Embedding:            embedding,
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
		CursorPublicationID: pgtype.UUID{Bytes: publicationUUID, Valid: true},
		CursorOccurredAt:    timePtr(canonical(source.OccurredAt)),
		UpdatedAt:           canonical(now),
	}); err != nil {
		return storeError("advance consumer cursor", err)
	}
	return nil
}

// cursorWorkerID names the durable consumer cursor row.
const cursorWorkerID = "projection"

const tombstoneOperation = "project.tombstone"

// Search runs one bounded deterministic lexical page over the active
// generation and revalidates every stored fact it returns.
func (r *Repository) Search(ctx context.Context, query domain.SearchQuery) (domain.SearchPage, error) {
	if query.Ranking == 0 {
		query.Ranking = domain.RankingLexical
	}
	if !domain.ValidRanking(query.Ranking) {
		return domain.SearchPage{}, domain.ErrInvalid
	}
	generation, err := r.ActiveGenerationID(ctx)
	if err != nil {
		return domain.SearchPage{}, err
	}
	snapshot := canonical(time.Now().UTC())
	// First-page sentinels: everything ranks below 1e9 and sorts after the
	// nil uuid, so the cursor predicate includes the whole page.
	cursorScore := 1e9
	var cursorCreated time.Time
	cursorSource := uuid.Nil.String()
	if query.Decoded != nil {
		if query.Decoded.RankingVersion != query.Ranking {
			// A token from one ranking never paginates another.
			return domain.SearchPage{}, domain.ErrInvalid
		}
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
	if strings.TrimSpace(domain.LexicalQueryText(query.CanonicalQuery)) == "" {
		return domain.SearchPage{GenerationID: generation}, nil
	}
	rows, err := r.queries.SearchProjectDocuments(ctx, indexerdb.SearchProjectDocumentsParams{
		GenerationID:    generation,
		OwnerUserID:     query.OwnerUserID,
		ProjectID:       query.ProjectID,
		QueryText:       domain.LexicalQueryText(query.CanonicalQuery),
		SourceType:      query.SourceType,
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
		document := domain.Document{
			OwnerUserID: query.OwnerUserID, ProjectID: query.ProjectID,
			SourceID: row.SourceID, SourceDigest: row.SourceDigest,
			ArtifactType: row.ArtifactType, Title: row.Title, Content: row.Content,
			SourceCreatedAt: row.SourceCreatedAt, LastPublication: row.LastPublicationID,
			IndexedAt: row.IndexedAt,
		}
		if domain.ValidStoredDocument(document) != nil {
			return domain.SearchPage{}, domain.ErrCorrupt
		}
		page.Hits = append(page.Hits, domain.SearchHit{
			ContextRef:   domain.ContextRef(row.SourceType, row.SourceID, row.SourceDigest),
			Excerpt:      domain.BuildExcerpt(domain.ExcerptRequest{Content: row.Content, Terms: queryTerms(query.CanonicalQuery)}),
			Score:        float64(row.Score),
			ArtifactID:   row.SourceID,
			SourceType:   row.SourceType,
			ArtifactType: row.ArtifactType,
			Digest:       row.SourceDigest,
			Title:        row.Title,
			CreatedAt:    row.SourceCreatedAt,
		})
	}
	if more {
		last := rows[len(rows)-1]
		page.Continuation = &domain.PageToken{
			OwnerUserID: query.OwnerUserID, ProjectID: query.ProjectID,
			QueryDigest: query.QueryDigest, RankingVersion: query.Ranking,
			GenerationID: generation, SnapshotThrough: snapshot,
			LastScore: float64(last.Score), LastSourceCreated: last.SourceCreatedAt, LastSourceID: last.SourceID,
		}
	}
	return page, nil
}

// hybridCandidateLimit bounds the per-scope semantic candidate fetch
// (ADR-0017 §3: single-owner local scale, ≤2000 documents per generation).
const hybridCandidateLimit = 2000

// SearchHybrid runs one bounded deterministic hybrid page (ADR-0017): the
// per-scope candidate fetch carries both the lexical ts_rank and the stored
// feature-hash embedding; cosine, the 0.5 lexical-normalized + 0.5 cosine
// fusion, the fused DESC / created DESC / source_id ASC ordering, and cursor
// pagination are computed in the indexer from the same bounded candidate set,
// so every page of one chain sees identical deterministic ordering.
func (r *Repository) SearchHybrid(ctx context.Context, query domain.SearchQuery) (domain.SearchPage, error) {
	if query.Ranking == 0 {
		query.Ranking = domain.RankingHybrid
	}
	if !domain.ValidRanking(query.Ranking) {
		return domain.SearchPage{}, domain.ErrInvalid
	}
	generation, err := r.ActiveGenerationID(ctx)
	if err != nil {
		return domain.SearchPage{}, err
	}
	snapshot := canonical(time.Now().UTC())
	var cursorCreated time.Time
	cursorSource := uuid.Nil.String()
	cursorScore := math.Inf(1)
	if query.Decoded != nil {
		if query.Decoded.RankingVersion != query.Ranking {
			return domain.SearchPage{}, domain.ErrInvalid
		}
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
	if strings.TrimSpace(domain.LexicalQueryText(query.CanonicalQuery)) == "" {
		return domain.SearchPage{GenerationID: generation}, nil
	}
	rows, err := r.queries.SearchProjectDocumentsHybrid(ctx, indexerdb.SearchProjectDocumentsHybridParams{
		GenerationID:    generation,
		OwnerUserID:     query.OwnerUserID,
		ProjectID:       query.ProjectID,
		QueryText:       domain.LexicalQueryText(query.CanonicalQuery),
		SourceType:      query.SourceType,
		SnapshotThrough: snapshot,
		RowLimit:        hybridCandidateLimit,
	})
	if err != nil {
		return domain.SearchPage{}, storeError("search documents hybrid", err)
	}
	queryVector := domain.Embed(query.CanonicalQuery)
	type candidate struct {
		row   indexerdb.SearchProjectDocumentsHybridRow
		fused float64
	}
	candidates := make([]candidate, 0, len(rows))
	maxLexical := 0.0
	for _, row := range rows {
		if row.LexicalScore > maxLexical {
			maxLexical = row.LexicalScore
		}
		candidates = append(candidates, candidate{row: row})
	}
	for i := range candidates {
		row := candidates[i].row
		lexical := 0.0
		if maxLexical > 0 {
			lexical = row.LexicalScore / maxLexical
		}
		semantic := 0.0
		if len(row.Embedding) == domain.EmbeddingDimensions {
			var docVector [domain.EmbeddingDimensions]float32
			copy(docVector[:], row.Embedding)
			semantic = float64(domain.CosineSimilarity(queryVector, docVector))
			if semantic < 0 || domain.IsDisallowedScore(semantic) {
				// Feature-hash cosine is bounded in [-1,1]; a negative value
				// contributes no recall, and any non-finite value is treated
				// the same way rather than poisoning the ordering.
				semantic = 0
			}
		}
		candidates[i].fused = 0.5*lexical + 0.5*semantic
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.fused != b.fused {
			return a.fused > b.fused
		}
		if !a.row.SourceCreatedAt.Equal(b.row.SourceCreatedAt) {
			return a.row.SourceCreatedAt.After(b.row.SourceCreatedAt)
		}
		return a.row.SourceID < b.row.SourceID
	})
	page := domain.SearchPage{GenerationID: generation, SnapshotThrough: snapshot}
	emitted := 0
	var lastEmitted candidate
	hasProbe := false
	for _, item := range candidates {
		row := item.row
		if item.fused <= 0 {
			break
		}
		after := item.fused < cursorScore ||
			(item.fused == cursorScore &&
				(row.SourceCreatedAt.After(cursorCreated) ||
					(row.SourceCreatedAt.Equal(cursorCreated) && row.SourceID > cursorSource)))
		if !after {
			continue
		}
		if emitted == query.PageSize {
			// Limit+1 probe: one more row exists after a full page, so the
			// continuation anchors at the last emitted hit (the lexical
			// cursor grammar excludes its own anchor row on the next page).
			// A page that drains the candidates exactly produces no phantom
			// token.
			hasProbe = true
			break
		}
		if domain.ValidStoredScore(item.fused) != nil {
			return domain.SearchPage{}, domain.ErrCorrupt
		}
		document := domain.Document{
			OwnerUserID: query.OwnerUserID, ProjectID: query.ProjectID,
			SourceID: row.SourceID, SourceDigest: row.SourceDigest,
			ArtifactType: row.ArtifactType, Title: row.Title, Content: row.Content,
			SourceCreatedAt: row.SourceCreatedAt, LastPublication: row.LastPublicationID,
			IndexedAt: row.IndexedAt,
		}
		if domain.ValidStoredDocument(document) != nil {
			return domain.SearchPage{}, domain.ErrCorrupt
		}
		page.Hits = append(page.Hits, domain.SearchHit{
			ContextRef:   domain.ContextRef(row.SourceType, row.SourceID, row.SourceDigest),
			Excerpt:      domain.BuildExcerpt(domain.ExcerptRequest{Content: row.Content, Terms: queryTerms(query.CanonicalQuery)}),
			Score:        item.fused,
			ArtifactID:   row.SourceID,
			SourceType:   row.SourceType,
			ArtifactType: row.ArtifactType,
			Digest:       row.SourceDigest,
			Title:        row.Title,
			CreatedAt:    row.SourceCreatedAt,
		})
		lastEmitted = item
		emitted++
	}
	if hasProbe {
		page.Continuation = &domain.PageToken{
			OwnerUserID: query.OwnerUserID, ProjectID: query.ProjectID,
			QueryDigest: query.QueryDigest, RankingVersion: query.Ranking,
			GenerationID: generation, SnapshotThrough: snapshot,
			LastScore: lastEmitted.fused, LastSourceCreated: lastEmitted.row.SourceCreatedAt, LastSourceID: lastEmitted.row.SourceID,
		}
	}
	return page, nil
}

// Freshness reads the bounded freshness projection from durable facts: the
// consumed publication watermark (cursor), the newest indexed document, and
// the Core-side pending count.
func (r *Repository) Freshness(ctx context.Context, pending int64) (domain.Freshness, error) {
	if pending < 0 {
		return domain.Freshness{}, domain.ErrCorrupt
	}
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

// DocumentStatus reports the active-generation state of one source.
func (r *Repository) DocumentStatus(ctx context.Context, ownerUserID, projectID, sourceID string) (ports.DocumentStatus, error) {
	if !domain.ValidUUID(ownerUserID) || !domain.ValidUUID(projectID) || !domain.ValidUUID(sourceID) {
		return ports.DocumentStatus{}, domain.ErrInvalid
	}
	row, err := r.queries.GetDocumentStatus(ctx, indexerdb.GetDocumentStatusParams{
		OwnerUserID: ownerUserID, ProjectID: projectID, SourceID: sourceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.DocumentStatus{}, nil
	}
	if err != nil {
		return ports.DocumentStatus{}, storeError("read document status", err)
	}
	if !domain.ValidDigest(row.SourceDigest) {
		return ports.DocumentStatus{}, domain.ErrCorrupt
	}
	return ports.DocumentStatus{Known: true, Digest: row.SourceDigest, Tombstoned: row.TombstonedAt != nil}, nil
}

// InsertWorkspaceSource binds (or rebinds) the owner's mount for one project
// and reactivates it. One scope carries at most one source by durable
// constraint.
func (r *Repository) InsertWorkspaceSource(ctx context.Context, source ports.WorkspaceSource) (ports.WorkspaceSource, error) {
	if !domain.ValidUUID(source.OwnerUserID) || !domain.ValidUUID(source.ProjectID) ||
		!domain.ValidWorkspaceRoot(source.RootPath) {
		return ports.WorkspaceSource{}, domain.ErrInvalid
	}
	now := canonical(time.Now().UTC())
	row, err := r.queries.InsertWorkspaceSource(ctx, indexerdb.InsertWorkspaceSourceParams{
		ID: source.ID, OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID,
		RootPath: source.RootPath, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return ports.WorkspaceSource{}, storeError("insert workspace source", err)
	}
	return workspaceSourceRow(row), nil
}

// GetWorkspaceSource reads one bound source; a miss is ErrNotFound.
func (r *Repository) GetWorkspaceSource(ctx context.Context, id string) (ports.WorkspaceSource, error) {
	if !domain.ValidUUID(id) {
		return ports.WorkspaceSource{}, domain.ErrInvalid
	}
	row, err := r.queries.GetWorkspaceSource(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.WorkspaceSource{}, domain.ErrNotFound
	}
	if err != nil {
		return ports.WorkspaceSource{}, storeError("read workspace source", err)
	}
	return workspaceSourceRow(row), nil
}

// ListWorkspaceSources reads every bound source.
func (r *Repository) ListWorkspaceSources(ctx context.Context) ([]ports.WorkspaceSource, error) {
	rows, err := r.queries.ListWorkspaceSources(ctx)
	if err != nil {
		return nil, storeError("list workspace sources", err)
	}
	sources := make([]ports.WorkspaceSource, 0, len(rows))
	for _, row := range rows {
		sources = append(sources, workspaceSourceRow(row))
	}
	return sources, nil
}

// SetWorkspaceSourceStatus rejects an outcome from a scan of an older binding.
func (r *Repository) SetWorkspaceSourceStatus(ctx context.Context, source ports.WorkspaceSource, status, degradedReason string, now time.Time) (ports.WorkspaceSource, error) {
	if !domain.ValidUUID(source.ID) || !domain.ValidWorkspaceSourceStatus(status) || source.UpdatedAt.IsZero() || now.IsZero() || (status == domain.WorkspaceDegraded && degradedReason == "") {
		return ports.WorkspaceSource{}, domain.ErrInvalid
	}
	row, err := r.queries.SetWorkspaceSourceStatus(ctx, indexerdb.SetWorkspaceSourceStatusParams{
		ID: source.ID, Status: status, DegradedReason: degradedReason, UpdatedAt: canonical(now), ExpectedUpdatedAt: canonical(source.UpdatedAt),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.WorkspaceSource{}, domain.ErrWorkspaceConflict
	}
	if err != nil {
		return ports.WorkspaceSource{}, storeError("set workspace source status", err)
	}
	return workspaceSourceRow(row), nil
}

// ConvergeWorkspacePass atomically commits the complete scan and its source
// status. Source locking plus version comparison reject overlapping stale scans.
func (r *Repository) ConvergeWorkspacePass(ctx context.Context, source ports.WorkspaceSource, files []ports.MountFile, skipped int64, passPublication func() string, now time.Time) (updated ports.WorkspaceSource, applied, tombstoned int64, err error) {
	if !domain.ValidUUID(source.ID) || !domain.ValidUUID(source.OwnerUserID) || !domain.ValidUUID(source.ProjectID) || source.UpdatedAt.IsZero() || now.IsZero() || skipped < 0 || len(files) > domain.WorkspaceMaxFiles || passPublication == nil {
		return ports.WorkspaceSource{}, 0, 0, domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ports.WorkspaceSource{}, 0, 0, storeError("begin workspace pass", err)
	}
	defer tx.Rollback(ctx)
	queries := r.queries.WithTx(tx)
	current, err := queries.GetWorkspaceSourceForUpdate(ctx, source.ID)
	if err != nil {
		return ports.WorkspaceSource{}, 0, 0, storeError("lock workspace source", err)
	}
	if current.OwnerUserID != source.OwnerUserID || current.ProjectID != source.ProjectID || current.RootPath != source.RootPath || current.Status == domain.WorkspaceStopped || !current.UpdatedAt.Equal(canonical(source.UpdatedAt)) {
		return ports.WorkspaceSource{}, 0, 0, domain.ErrWorkspaceConflict
	}
	if _, err := queries.LockActiveGeneration(ctx); err != nil {
		return ports.WorkspaceSource{}, 0, 0, storeError("lock active generation", err)
	}
	if err := queries.LockIndexProject(ctx, source.OwnerUserID+"/"+source.ProjectID); err != nil {
		return ports.WorkspaceSource{}, 0, 0, storeError("lock index project", err)
	}
	writable, err := queries.WritableGenerationIDs(ctx)
	if err != nil {
		return ports.WorkspaceSource{}, 0, 0, storeError("read writable generations", err)
	}
	if len(writable) == 0 {
		return ports.WorkspaceSource{}, 0, 0, domain.ErrCorrupt
	}
	for _, file := range files {
		// One fresh monotonic publication per file: receipt arbitration keys
		// on (publication, generation), so a shared pass id would collapse
		// every file after the first into a replay. The canonical effect
		// digest is the file content digest.
		resolved := ports.ResolvedSource{
			Verdict: "resolved", Operation: "workspace.upsert",
			OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID,
			ArtifactID: file.SourceID, ArtifactType: "workspace.text.v1",
			Digest: file.Digest, Title: file.Title, Content: file.Content,
			CreatedAt: now, PublicationID: passPublication(), OccurredAt: now,
		}
		if applyErr := applyResolvedSource(ctx, queries, resolved, domain.OutcomeApplied,
			file.Digest, now); applyErr != nil {
			return ports.WorkspaceSource{}, 0, 0, applyErr
		}
		applied++
	}
	// Set-difference tombstones per writable generation: a live workspace
	// document the pass no longer sees leaves the search projection.
	kept := make(map[string]bool, len(files))
	for _, file := range files {
		kept[file.SourceID] = true
	}
	for _, generation := range writable {
		live, listErr := queries.ListLiveWorkspaceDocuments(ctx, indexerdb.ListLiveWorkspaceDocumentsParams{
			GenerationID: generation, OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID,
		})
		if listErr != nil {
			return ports.WorkspaceSource{}, 0, 0, storeError("list live workspace documents", listErr)
		}
		for _, row := range live {
			if kept[row.SourceID] {
				continue
			}
			rows, tombErr := queries.TombstoneWorkspaceDocument(ctx, indexerdb.TombstoneWorkspaceDocumentParams{
				GenerationID: generation, OwnerUserID: source.OwnerUserID,
				ProjectID: source.ProjectID, SourceID: row.SourceID,
				TombstonedAt: timePtr(canonical(now)), UpdatedAt: canonical(now),
			})
			if tombErr != nil {
				return ports.WorkspaceSource{}, 0, 0, storeError("tombstone workspace document", tombErr)
			}
			if rows != 1 {
				return ports.WorkspaceSource{}, 0, 0, domain.ErrCorrupt
			}
			tombstoned++
		}
	}

	row, err := queries.RecordWorkspaceSync(ctx, indexerdb.RecordWorkspaceSyncParams{
		ID: source.ID, IndexedCount: applied, SkippedCount: skipped, TombstonedCount: tombstoned,
		LastSyncedAt: timePtr(canonical(now)), UpdatedAt: canonical(now),
	})
	if err != nil {
		return ports.WorkspaceSource{}, 0, 0, storeError("record workspace sync", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ports.WorkspaceSource{}, 0, 0, storeError("commit workspace pass", err)
	}
	return workspaceSourceRow(row), applied, tombstoned, nil
}

func workspaceSourceRow(row indexerdb.WorkosIndexWorkspaceSource) ports.WorkspaceSource {
	source := ports.WorkspaceSource{
		ID: row.ID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
		RootPath: row.RootPath, Status: row.Status, DegradedReason: row.DegradedReason,
		IndexedCount: row.IndexedCount, SkippedCount: row.SkippedCount,
		TombstonedCount: row.TombstonedCount,
		CreatedAt:       row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.LastSyncedAt != nil {
		source.LastSyncedAt = *row.LastSyncedAt
	}
	return source
}

// PutArchiveObject stores one bounded object, content-addressed per owner.
// A repeated digest returns the existing object id with inserted=false.
func (r *Repository) PutArchiveObject(ctx context.Context, ownerUserID, digest, mediaType string, content []byte, now time.Time) (ports.ArchiveObject, bool, error) {
	if !domain.ValidUUID(ownerUserID) || !domain.ValidDigest(digest) ||
		!domain.ValidArchiveMediaType(mediaType) ||
		len(content) == 0 || len(content) > domain.ArchiveMaxObjectBytes {
		return ports.ArchiveObject{}, false, domain.ErrInvalid
	}
	row, err := r.queries.UpsertArchiveObject(ctx, indexerdb.UpsertArchiveObjectParams{
		ID: r.ids.New(), OwnerUserID: ownerUserID, Sha256: digest,
		MediaType: mediaType, ByteCount: int64(len(content)),
		Bytes: content, CreatedAt: canonical(now), UpdatedAt: canonical(now),
	})
	if err != nil {
		return ports.ArchiveObject{}, false, storeError("upsert archive object", err)
	}
	return ports.ArchiveObject{
		ID: row.ID, OwnerUserID: row.OwnerUserID, Sha256: row.Sha256,
		MediaType: row.MediaType, ByteCount: row.ByteCount, CreatedAt: row.CreatedAt,
	}, row.Inserted, nil
}

// GetArchiveObject reads one object with its bytes; a miss is ErrNotFound.
func (r *Repository) GetArchiveObject(ctx context.Context, ownerUserID, objectID string) (ports.ArchiveObject, []byte, error) {
	if !domain.ValidUUID(ownerUserID) || !domain.ValidUUID(objectID) {
		return ports.ArchiveObject{}, nil, domain.ErrInvalid
	}
	row, err := r.queries.GetArchiveObject(ctx, indexerdb.GetArchiveObjectParams{
		OwnerUserID: ownerUserID, ID: objectID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ArchiveObject{}, nil, domain.ErrNotFound
	}
	if err != nil {
		return ports.ArchiveObject{}, nil, storeError("get archive object", err)
	}
	if int64(len(row.Bytes)) != row.ByteCount {
		return ports.ArchiveObject{}, nil, domain.ErrCorrupt
	}
	return ports.ArchiveObject{
		ID: row.ID, OwnerUserID: row.OwnerUserID, Sha256: row.Sha256,
		MediaType: row.MediaType, ByteCount: row.ByteCount, CreatedAt: row.CreatedAt,
	}, row.Bytes, nil
}

// ListArchiveObjects reads one bounded metadata page.
func (r *Repository) ListArchiveObjects(ctx context.Context, ownerUserID string, limit int) ([]ports.ArchiveObject, error) {
	if limit <= 0 || limit > 200 {
		return nil, domain.ErrInvalid
	}
	rows, err := r.queries.ListArchiveObjects(ctx, indexerdb.ListArchiveObjectsParams{
		OwnerUserID: ownerUserID, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, storeError("list archive objects", err)
	}
	objects := make([]ports.ArchiveObject, 0, len(rows))
	for _, row := range rows {
		objects = append(objects, ports.ArchiveObject{
			ID: row.ID, OwnerUserID: row.OwnerUserID, Sha256: row.Sha256,
			MediaType: row.MediaType, ByteCount: row.ByteCount, CreatedAt: row.CreatedAt,
		})
	}
	return objects, nil
}

// CountArchiveObjects returns the owner's object count for the bound check.
func (r *Repository) CountArchiveObjects(ctx context.Context, ownerUserID string) (int64, error) {
	return r.queries.CountArchiveObjects(ctx, ownerUserID)
}

func (r *Repository) ReadDocument(ctx context.Context, input domain.DocumentRead) (domain.Document, error) {
	row, err := r.queries.ReadIndexedDocument(ctx, indexerdb.ReadIndexedDocumentParams{
		OwnerUserID: input.OwnerUserID, ProjectID: input.ProjectID, SourceType: input.SourceType, SourceID: input.SourceID, Digest: input.Digest,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Document{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Document{}, storeError("read indexed document", err)
	}
	doc := domain.Document{OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID, SourceID: row.SourceID, SourceDigest: row.SourceDigest,
		ArtifactType: row.ArtifactType, Title: row.Title, Content: row.Content, SourceCreatedAt: row.SourceCreatedAt,
		LastPublication: row.LastPublicationID, IndexedAt: row.IndexedAt}
	if err := domain.ValidStoredDocument(doc); err != nil {
		return domain.Document{}, err
	}
	return doc, nil
}
