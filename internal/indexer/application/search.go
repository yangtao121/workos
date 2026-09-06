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
	SourceType  string
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

// SearchHybrid runs the fused lexical+cosine ranking (ADR-0017): the
// projection fuses normalized lexical ts_rank with deterministic feature-hash
// cosine similarity and orders by fused score with the same deterministic
// tie-breaks and page-token chain as the lexical ranking.
func (s *SearchService) SearchHybrid(ctx context.Context, input SearchInput) (SearchResult, error) {
	return s.run(ctx, input, domain.RankingHybrid)
}

func (s *SearchService) Search(ctx context.Context, input SearchInput) (SearchResult, error) {
	return s.run(ctx, input, domain.RankingLexical)
}

func (s *SearchService) run(ctx context.Context, input SearchInput, ranking int) (SearchResult, error) {
	if !domain.ValidUUID(input.OwnerUserID) || !domain.ValidUUID(input.ProjectID) {
		return SearchResult{}, domain.ErrInvalid
	}
	if input.PageSize < 0 || (input.SourceType != "" && input.SourceType != domain.SourceReviewArtifact && input.SourceType != domain.SourceWorkspaceFile) {
		return SearchResult{}, domain.ErrInvalid
	}
	canonicalQuery, err := domain.CanonicalQuery(input.RawQuery)
	if err != nil {
		return SearchResult{}, err
	}
	digestQuery := canonicalQuery
	if input.SourceType != "" {
		digestQuery += "\x00" + input.SourceType
	}
	query := domain.SearchQuery{
		SourceType:     input.SourceType,
		OwnerUserID:    input.OwnerUserID,
		ProjectID:      input.ProjectID,
		CanonicalQuery: canonicalQuery,
		QueryDigest:    domain.QueryDigest(input.OwnerUserID, input.ProjectID, digestQuery),
		PageSize:       domain.ClampSearchPageSize(input.PageSize),
		Ranking:        ranking,
	}
	if input.PageToken != "" {
		token, err := s.tokens.Decode(input.PageToken)
		if err != nil {
			return SearchResult{}, err
		}
		// Token bindings are re-verified against the live request: any
		// cross-scope, cross-query, cross-ranking, or cross-version replay is
		// invalid.
		if token.OwnerUserID != input.OwnerUserID || token.ProjectID != input.ProjectID ||
			token.QueryDigest != query.QueryDigest || token.RankingVersion != ranking {
			return SearchResult{}, domain.ErrInvalidPageToken
		}
		query.Decoded = &token
		query.TokenRaw = input.PageToken
	}
	var page domain.SearchPage
	if ranking == domain.RankingHybrid {
		page, err = s.projection.SearchHybrid(ctx, query)
	} else {
		page, err = s.projection.Search(ctx, query)
	}
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

func (s *SearchService) ReadDocument(ctx context.Context, input domain.DocumentRead) (domain.Document, error) {
	if !domain.ValidUUID(input.OwnerUserID) || !domain.ValidUUID(input.ProjectID) || !domain.ValidUUID(input.SourceID) || !domain.ValidDigest(input.Digest) ||
		(input.SourceType != domain.SourceReviewArtifact && input.SourceType != domain.SourceWorkspaceFile) {
		return domain.Document{}, domain.ErrInvalid
	}
	return s.projection.ReadDocument(ctx, input)
}
