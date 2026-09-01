// Index publication feed application service: the Core-side authority the
// indexer consumes over the private IndexPublicationSourceService. Claims
// are database-arbitrated leases with opaque server-minted tokens; resolves
// re-verify every immutable fact from the owning modules inside the claim's
// transaction; completes record terminal outcomes only for live claims.
// Transient outages stay retryable and are never folded into terminal
// outcomes (ADR-0013).
package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/core/indexfeed/domain"
	"github.com/yangtao121/workos/internal/core/indexfeed/ports"
	dbtransient "github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

var (
	ErrInvalidClaim = errors.New("index feed claim request is invalid")
)

// TxSource opens the resolve transaction. *pgxpool.Pool satisfies it.
type TxSource interface {
	Begin(ctx context.Context) (dbtx.Tx, error)
}

// Service composes the claim store and the neutral source authority.
type Service struct {
	store     ports.PublicationStore
	authority ports.SourceAuthority
	pool      TxSource
	now       func() time.Time
}

func NewService(store ports.PublicationStore, authority ports.SourceAuthority, pool TxSource) (*Service, error) {
	if store == nil || authority == nil || pool == nil {
		return nil, errors.New("index feed service requires store, source authority, and tx source")
	}
	return &Service{store: store, authority: authority, pool: pool, now: func() time.Time { return time.Now().UTC() }}, nil
}

// ClaimInput is one bounded claim request.
type ClaimInput struct {
	WorkerID     string
	MaxBatch     int32
	LeaseSeconds int32
}

// ClaimedPublication is one lease handed to a worker.
type ClaimedPublication struct {
	Publication domain.Publication
	LeaseToken  string
	ExpiresAt   time.Time
}

// Claim leases up to MaxBatch pending publications. The lease token is
// minted server-side per claim; it never appears in logs or public
// responses.
func (s *Service) Claim(ctx context.Context, input ClaimInput) ([]ClaimedPublication, error) {
	if !domain.ValidWorkerID(input.WorkerID) {
		return nil, ErrInvalidClaim
	}
	maxBatch := int(input.MaxBatch)
	if maxBatch <= 0 {
		maxBatch = 1
	}
	if maxBatch > domain.MaxClaimBatch {
		return nil, ErrInvalidClaim
	}
	lease := domain.ClampLeaseSeconds(input.LeaseSeconds)
	now := domain.CanonicalUTCTime(s.now())
	token, err := newClaimToken()
	if err != nil {
		return nil, fmt.Errorf("mint claim token: %w", err)
	}
	claimed, err := s.store.ClaimPending(ctx, input.WorkerID, token, now.Add(lease), now, maxBatch)
	if err != nil {
		return nil, err
	}
	out := make([]ClaimedPublication, 0, len(claimed))
	for _, item := range claimed {
		out = append(out, ClaimedPublication{Publication: item.Publication, LeaseToken: token, ExpiresAt: item.ExpiresAt})
	}
	return out, nil
}

// ResolveVerdict is the authoritative classification of one claimed
// publication plus, for resolved upserts, the verified bounded source.
type ResolveVerdict struct {
	Verdict     string // "resolved" | "tombstoned" | "corrupt" | "unsupported"
	Publication domain.Publication
	Source      *ports.VerifiedSource
}

// Resolve re-verifies one live claim from Core authority inside the claim's
// transaction. A tombstone publication classifies itself; an upsert whose
// project was archived concurrently classifies as tombstoned by the
// authoritative lifecycle; any identity/digest drift is terminal corruption.
// The verified content leaves Core exactly once per resolve.
func (s *Service) Resolve(ctx context.Context, workerID, publicationID, leaseToken string) (ResolveVerdict, error) {
	if !domain.ValidWorkerID(workerID) || !domain.ValidUUID(publicationID) || leaseToken == "" {
		return ResolveVerdict{}, ErrInvalidClaim
	}
	now := domain.CanonicalUTCTime(s.now())
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ResolveVerdict{}, storeFailure("begin index publication resolve", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	publication, err := s.store.LockForResolve(ctx, tx, publicationID, workerID, leaseToken, now)
	if err != nil {
		return ResolveVerdict{}, err
	}
	verdict := ResolveVerdict{Publication: publication}
	switch publication.Operation {
	case domain.OperationProjectTombstone:
		verdict.Verdict = "tombstoned"
	case domain.OperationReviewArtifactUpsert:
		source, sourceErr := s.authority.ResolveReviewSource(ctx, tx, publication.OwnerUserID, publication.ProjectID, publication.SourceID, publication.Digest)
		switch {
		case sourceErr == nil:
			verdict.Verdict = "resolved"
			verdict.Source = &source
		case errors.Is(sourceErr, ports.ErrSourceArchived):
			verdict.Verdict = "tombstoned"
		case errors.Is(sourceErr, ports.ErrSourceUnsupported):
			verdict.Verdict = "unsupported"
		case errors.Is(sourceErr, ports.ErrSourceCorrupt):
			verdict.Verdict = "corrupt"
		default:
			return ResolveVerdict{}, storeFailure("resolve index publication source", sourceErr)
		}
	default:
		return ResolveVerdict{}, domain.ErrCorrupt
	}
	if err := tx.Commit(ctx); err != nil {
		return ResolveVerdict{}, storeFailure("commit index publication resolve", err)
	}
	return verdict, nil
}

// CompleteInput is one bounded batch of locally recorded outcomes.
type CompleteInput struct {
	WorkerID string
	Results  []CompleteEntry
}

// CompleteEntry is one outcome; the lease token proves the live claim.
type CompleteEntry struct {
	PublicationID string
	LeaseToken    string
	Outcome       string
}

// AckedResult reports whether this worker's claim was still live when the
// outcome was recorded. Stale entries stay false: the consumer's durable
// receipt turns the inevitable replay into a no-op.
type AckedResult struct {
	PublicationID string
	Acked         bool
}

func (s *Service) Complete(ctx context.Context, input CompleteInput) ([]AckedResult, error) {
	if !domain.ValidWorkerID(input.WorkerID) {
		return nil, ErrInvalidClaim
	}
	if len(input.Results) == 0 || len(input.Results) > domain.MaxClaimBatch {
		return nil, ErrInvalidClaim
	}
	results := make([]ports.CompleteResult, 0, len(input.Results))
	for _, entry := range input.Results {
		if !domain.ValidUUID(entry.PublicationID) || entry.LeaseToken == "" {
			return nil, ErrInvalidClaim
		}
		switch entry.Outcome {
		case domain.OutcomeCompleted, domain.OutcomeTombstoned, domain.OutcomeUnsupported, domain.OutcomeCorrupt:
		default:
			return nil, ErrInvalidClaim
		}
		results = append(results, ports.CompleteResult{
			PublicationID: entry.PublicationID, LeaseToken: entry.LeaseToken, Outcome: entry.Outcome,
		})
	}
	now := domain.CanonicalUTCTime(s.now())
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, storeFailure("begin index publication complete", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	acked, err := s.store.Complete(ctx, tx, input.WorkerID, results, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, storeFailure("commit index publication complete", err)
	}
	out := make([]AckedResult, 0, len(results))
	for _, result := range results {
		out = append(out, AckedResult{PublicationID: result.PublicationID, Acked: acked[result.PublicationID]})
	}
	return out, nil
}

func newClaimToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// storeFailure classifies a raw begin/commit failure at the application
// boundary: transient dependency failures carry the port sentinel so the
// transport answers a sanitized Unavailable.
func storeFailure(stage string, err error) error {
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", stage, ports.ErrStoreUnavailable, err)
	}
	return fmt.Errorf("%s: %w", stage, err)
}

// ReconcilePage is one page of authoritative active-project review artifact
// identity facts. Content is resolved separately and digest-pinned.
type ReconcilePage struct {
	Sources   []ports.SourceSummary
	NextToken string
}

// ReconcileSources pages the authoritative active review artifacts through
// the neutral source authority. The returned page can be shorter than the
// requested size (archived-project rows are dropped); the continuation
// cursor decides whether more pages exist.
func (s *Service) ReconcileSources(ctx context.Context, pageSize int32, cursor string) (ReconcilePage, error) {
	size := int(pageSize)
	if size <= 0 || size > 200 {
		return ReconcilePage{}, ErrInvalidClaim
	}
	if cursor == "" {
		cursor = firstPageReconcileCursor
	}
	sources, next, err := s.authority.ReconcileSources(ctx, size, cursor)
	if err != nil {
		return ReconcilePage{}, err
	}
	return ReconcilePage{Sources: sources, NextToken: next}, nil
}

// ArchivedProjectPage is one page of archived project scopes.
type ArchivedProjectPage struct {
	Projects  []ports.ArchivedProject
	NextToken string
}

// ReconcileArchivedProjects pages archived project scopes for the feed's
// tombstone convergence.
func (s *Service) ReconcileArchivedProjects(ctx context.Context, pageSize int32, cursor string) (ArchivedProjectPage, error) {
	size := int(pageSize)
	if size <= 0 || size > 200 {
		return ArchivedProjectPage{}, ErrInvalidClaim
	}
	if cursor == "" {
		cursor = firstPageReconcileCursor
	}
	projects, next, err := s.authority.ReconcileArchivedProjects(ctx, size, cursor)
	if err != nil {
		return ArchivedProjectPage{}, err
	}
	return ArchivedProjectPage{Projects: projects, NextToken: next}, nil
}

// SourceContent is one digest-pinned verified content resolution.
type SourceContent struct {
	Source ports.VerifiedSource
}

// ResolveSourceContent resolves bounded verified content for a specific
// authoritative artifact (reconciliation repair and rebuild snapshotting).
func (s *Service) ResolveSourceContent(ctx context.Context, ownerUserID, projectID, artifactID, expectedDigest string) (ports.VerifiedSource, error) {
	if !domain.ValidUUID(ownerUserID) || !domain.ValidUUID(projectID) || !domain.ValidUUID(artifactID) || !domain.ValidDigest(expectedDigest) {
		return ports.VerifiedSource{}, ErrInvalidClaim
	}
	return s.authority.ResolveSourceContent(ctx, ownerUserID, projectID, artifactID, expectedDigest)
}

// firstPageReconcileCursor is the decoded form of "first page": version +
// zero time + empty id. Empty transport cursors are canonicalized here so
// the authority's decoder only ever sees well-formed tokens.
const firstPageReconcileCursor = "v1:0:"

// CountPending reports publications still awaiting a terminal outcome.
func (s *Service) CountPending(ctx context.Context) (int64, error) {
	return s.store.CountPending(ctx)
}
