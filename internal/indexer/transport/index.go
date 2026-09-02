// Public owner-facing IndexService transport (ADR-0013 §8): Search and the
// IndexContext repair job. Identity comes exclusively from the trusted
// gateway-injected context; request bodies carry only bounded input. The
// gateway allowlist covers exactly this service; the private source and
// admin services in the same package stay unroutable.
package transport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	indexv1connect "github.com/yangtao121/workos/gen/go/workos/index/v1/indexv1connect"
	indexerapp "github.com/yangtao121/workos/internal/indexer/application"
	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// IndexService is the application surface the handler serves.
type IndexService interface {
	Search(ctx context.Context, input indexerapp.SearchInput) (indexerapp.SearchResult, error)
	// CreateRepairJob adjudicates and persists one repair job.
	CreateRepairJob(ctx context.Context, input indexerapp.JobRequestInput) (indexerapp.JobView, bool, error)
	// GetRepairJob reads one owner-scoped job.
	GetRepairJob(ctx context.Context, ownerUserID, jobID string) (indexerapp.JobView, []indexerapp.JobSourceView, error)
}

type Handler struct {
	service IndexService
}

func NewHandler(service IndexService) *Handler {
	return &Handler{service: service}
}

// NewConnectHandler wires the public transport. The read limit is derived
// from the largest legal request: 32 typed refs plus a bounded query stay
// far below 64 KiB even with base64 inflation and gzip decompression.
func NewConnectHandler(service IndexService) (string, http.Handler) {
	return indexv1connect.NewIndexServiceHandler(
		NewHandler(service),
		connect.WithReadMaxBytes(64*1024),
	)
}

func (h *Handler) Search(ctx context.Context, req *connect.Request[indexv1.SearchRequest]) (*connect.Response[indexv1.SearchResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	pageSize := int32(0)
	if req.Msg.GetPage() != nil {
		pageSize = req.Msg.GetPage().GetPageSize()
	}
	result, err := h.service.Search(ctx, indexerapp.SearchInput{
		OwnerUserID: id.UserID,
		ProjectID:   req.Msg.GetProjectId(),
		RawQuery:    req.Msg.GetQuery(),
		PageSize:    pageSize,
		PageToken:   req.Msg.GetPage().GetPageToken(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	hits := make([]*indexv1.SearchHit, 0, len(result.Page.Hits))
	for _, hit := range result.Page.Hits {
		hits = append(hits, &indexv1.SearchHit{
			ContextRef:   hit.ContextRef,
			Excerpt:      hit.Excerpt,
			Score:        hit.Score,
			SourceRef:    &agentv1.ContextRef{Type: "artifact.review.v1", Id: hit.ArtifactID, Revision: hit.Digest},
			ArtifactId:   hit.ArtifactID,
			ArtifactType: hit.ArtifactType,
			Digest:       hit.Digest,
			Title:        hit.Title,
			CreatedAt:    hit.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000Z07:00"),
		})
	}
	response := &indexv1.SearchResponse{
		Hits: hits,
		Page: &commonv1.PageResponse{NextPageToken: result.Page.NextPageToken},
		Freshness: &indexv1.IndexFreshness{
			CaughtUp:            result.Freshness.CaughtUp,
			IndexedThrough:      formatSearchTime(result.Freshness.IndexedThrough),
			LastIndexedAt:       formatSearchTime(result.Freshness.LastIndexedAt),
			PendingPublications: result.Freshness.PendingPublications,
		},
	}
	return connect.NewResponse(response), nil
}

func formatSearchTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("2006-01-02T15:04:05.000000Z07:00")
}

func (h *Handler) IndexContext(ctx context.Context, req *connect.Request[indexv1.IndexContextRequest]) (*connect.Response[indexv1.IndexContextResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	// Legacy free-form refs have no documented grammar and are never parsed:
	// any non-empty value fails closed (ADR-0013 §6).
	if len(req.Msg.GetContextRefs()) > 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("index context request is invalid"))
	}
	sources := make([]indexerapp.RepairSourceRef, 0, len(req.Msg.GetSources()))
	for _, source := range req.Msg.GetSources() {
		sources = append(sources, indexerapp.RepairSourceRef{
			ArtifactID: source.GetArtifactId(),
			Digest:     source.GetDigest(),
		})
	}
	key := req.Msg.GetIdempotencyKey()
	if len(key) == 0 || len(key) > 128 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("index context request is invalid"))
	}
	view, created, err := h.service.CreateRepairJob(ctx, indexerapp.JobRequestInput{
		OwnerUserID:    id.UserID,
		ProjectID:      req.Msg.GetProjectId(),
		IdempotencyKey: key,
		Sources:        sources,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&indexv1.IndexContextResponse{Job: jobProto(view, created)}), nil
}

func jobProto(view indexerapp.JobView, created bool) *indexv1.IndexJob {
	state := indexv1.IndexJobState_INDEX_JOB_STATE_PENDING
	switch view.State {
	case "running":
		state = indexv1.IndexJobState_INDEX_JOB_STATE_RUNNING
	case "completed":
		state = indexv1.IndexJobState_INDEX_JOB_STATE_COMPLETED
	case "failed":
		state = indexv1.IndexJobState_INDEX_JOB_STATE_FAILED
	}
	legacy := "pending"
	switch view.State {
	case "running":
		legacy = "running"
	case "completed":
		legacy = "completed"
	case "failed":
		legacy = "failed"
	}
	return &indexv1.IndexJob{
		Id:        view.ID,
		ProjectId: view.ProjectID,
		State:     legacy,
		JobState:  state,
		CreatedAt: view.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000Z07:00"),
		UpdatedAt: view.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000000Z07:00"),
	}
}

// mapError converts indexer verdicts to sanitized Connect codes: malformed
// input/tokens are InvalidArgument; outages are retryable Unavailable;
// stored corruption never leaks detail; project misses are indistinguishable
// from foreign ones and never from validation failures.
func mapError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid), errors.Is(err, domain.ErrInvalidQuery):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("index request is invalid"))
	case errors.Is(err, domain.ErrInvalidPageToken):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("index page token is invalid"))
	case errors.Is(err, indexerapp.ErrJobConflict):
		return connect.NewError(connect.CodeAborted, errors.New("idempotency key was already used for a different request"))
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, ports.ErrProjectNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("index scope is not available"))
	case errors.Is(err, domain.ErrUnavailable), errors.Is(err, ports.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("index service is temporarily unavailable"))
	case errors.Is(err, ports.ErrCoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("index service is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("index operation failed"))
	}
}
