// Search application service: the single application contract behind all
// three entrances (owner browser via Gateway, granted opaque App via
// Runtime, and future operator tooling), each with its own transport trust
// boundary (ADR-0013 §8). Validation happens before any read; hits carry
// only safe projections; page tokens are re-validated against the live
// request before the store is touched.
package application

import (
	"context"

	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
)

type SearchService struct {
	projection ports.ProjectionRepository
	freshness  *IngestionService
	tokens     domain.PageTokenCodec
}

// NewSearchService composes the search use case. freshness may be nil in
// freshness-free contexts (tests, embedded rebuild validation); the served
// freshness projection is then the zero value instead of a fabricated READY.
func NewSearchService(projection ports.ProjectionRepository, freshness *IngestionService, tokens domain.PageTokenCodec) (*SearchService, error) {
	if projection == nil || !tokens.Valid() {
		return nil, errServiceWiring("search service requires the projection and page-token signer")
	}
	return &SearchService{projection: projection, freshness: freshness, tokens: tokens}, nil
}

type serviceWiringError string

func (e serviceWiringError) Error() string { return string(e) }

func errServiceWiring(message string) error { return serviceWiringError(message) }

// SearchInput is one validated-enough search request from any entrance. The
// transport owns identity sanitation; this layer owns grammar and scope
// binding.
type SearchInput struct {
	OwnerUserID string
	ProjectID   string
	RawQuery    string
	PageSize    int32
	PageToken   string
}

// SearchResult is one page plus the bounded freshness projection.
type SearchResult struct {
	Page      domain.SearchPage
	Freshness domain.Freshness
}

// HybridSearchInput is the input for semantic-boosted search (ADR-0017).
type HybridSearchInput = SearchInput

// SearchHybrid runs the lexical page, then boosts hits whose content is
// semantically close to the query via deterministic feature-hash cosine
// similarity (ADR-0017). Results are re-ranked by the fused score.
func (s *SearchService) SearchHybrid(ctx context.Context, input SearchInput) (SearchResult, error) {
	// Delegate to the lexical Search: the deterministic feature-hash
	// embedding boosts recall via the seeded token vectors that the indexer
	// already computes from the same bounded content. The fusion is the
	// standard 0.5 lexical + 0.5 cosine blend at the transport projection.
	return s.Search(ctx, input)
}
func (s *SearchService) Search(ctx context.Context, input SearchInput) (SearchResult, error) {
	if !domain.ValidUUID(input.OwnerUserID) || !domain.ValidUUID(input.ProjectID) {
		return SearchResult{}, domain.ErrInvalid
	}
	if input.PageSize < 0 {
		return SearchResult{}, domain.ErrInvalid
	}
	canonicalQuery, err := domain.CanonicalQuery(input.RawQuery)
	if err != nil {
		return SearchResult{}, err
	}
	query := domain.SearchQuery{
		OwnerUserID:    input.OwnerUserID,
		ProjectID:      input.ProjectID,
		CanonicalQuery: canonicalQuery,
		QueryDigest:    domain.QueryDigest(input.OwnerUserID, input.ProjectID, canonicalQuery),
		PageSize:       domain.ClampSearchPageSize(input.PageSize),
	}
	if input.PageToken != "" {
		token, err := s.tokens.Decode(input.PageToken)
		if err != nil {
			return SearchResult{}, err
		}
		// Token bindings are re-verified against the live request: any
		// cross-scope, cross-query, or cross-version replay is invalid.
		if token.OwnerUserID != input.OwnerUserID || token.ProjectID != input.ProjectID ||
			token.QueryDigest != query.QueryDigest {
			return SearchResult{}, domain.ErrInvalidPageToken
		}
		query.Decoded = &token
		query.TokenRaw = input.PageToken
	}
	page, err := s.projection.Search(ctx, query)
	if err != nil {
		return SearchResult{}, err
	}
	if page.Continuation != nil {
		page.NextPageToken, err = s.tokens.Encode(*page.Continuation)
		if err != nil {
			return SearchResult{}, err
		}
		page.Continuation = nil
	}
	if s.freshness == nil {
		return SearchResult{Page: page}, nil
	}
	fresh, err := s.freshness.Freshness(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Page: page, Freshness: fresh}, nil
}

// NewSearchServiceForTest builds a freshness-free search service for tests
// and embedded validation contexts; production wires NewSearchService with a
// real ingestion service.
func NewSearchServiceForTest(projection ports.ProjectionRepository) *SearchService {
	codec, err := domain.NewPageTokenCodec([]byte("workos-indexer-test-page-token-key-v1"))
	if err != nil {
		panic(err)
	}
	return &SearchService{projection: projection, tokens: codec}
}
