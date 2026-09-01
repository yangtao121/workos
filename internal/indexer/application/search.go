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
}

func NewSearchService(projection ports.ProjectionRepository, freshness *IngestionService) (*SearchService, error) {
	if projection == nil || freshness == nil {
		return nil, errServiceWiring("search service requires projection and freshness dependencies")
	}
	return &SearchService{projection: projection, freshness: freshness}, nil
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

// Search validates the query grammar and page token, runs one lexical page,
// and attaches the freshness projection.
func (s *SearchService) Search(ctx context.Context, input SearchInput) (SearchResult, error) {
	if !domain.ValidUUID(input.OwnerUserID) || !domain.ValidUUID(input.ProjectID) {
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
		token, err := domain.DecodePageToken(input.PageToken)
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
	fresh, err := s.freshness.Freshness(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Page: page, Freshness: fresh}, nil
}
