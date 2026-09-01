// Private transport for the Core index publication source service. It is
// registered on the Core HTTP mux but never on the gateway allowlist, so
// browsers and apps deterministically cannot reach it; only the indexer's
// internal worker reaches these RPCs. Request bodies are tiny and bounded
// well below the pre-decode limits; responses carry the canonical bounded
// source snapshot exactly once per resolve.
package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	indexv1connect "github.com/yangtao121/workos/gen/go/workos/index/v1/indexv1connect"
	indexfeedapp "github.com/yangtao121/workos/internal/core/indexfeed/application"
	"github.com/yangtao121/workos/internal/core/indexfeed/domain"
	"github.com/yangtao121/workos/internal/core/indexfeed/ports"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler exposes the private source RPCs.
type Handler struct {
	service *indexfeedapp.Service
}

func NewHandler(service *indexfeedapp.Service) *Handler {
	return &Handler{service: service}
}

// NewConnectHandler wires the private transport. The read limits are derived
// from the largest legal request: claim/complete batches of 16 tiny facts
// and one resolve request of three identifiers stay far below 16 KiB even
// with base64 inflation.
func NewConnectHandler(service *indexfeedapp.Service) (string, http.Handler) {
	return indexv1connect.NewIndexPublicationSourceServiceHandler(
		NewHandler(service),
		connect.WithReadMaxBytes(16*1024),
	)
}

func (h *Handler) ClaimIndexPublications(ctx context.Context, req *connect.Request[indexv1.ClaimIndexPublicationsRequest]) (*connect.Response[indexv1.ClaimIndexPublicationsResponse], error) {
	claimed, err := h.service.Claim(ctx, indexfeedapp.ClaimInput{
		WorkerID:     req.Msg.GetWorkerId(),
		MaxBatch:     req.Msg.GetMaxBatch(),
		LeaseSeconds: req.Msg.GetLeaseSeconds(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	publications := make([]*indexv1.IndexPublication, 0, len(claimed))
	for _, item := range claimed {
		publications = append(publications, publicationProto(item.Publication, item.LeaseToken))
	}
	return connect.NewResponse(&indexv1.ClaimIndexPublicationsResponse{Publications: publications}), nil
}

func (h *Handler) ResolveIndexPublication(ctx context.Context, req *connect.Request[indexv1.ResolveIndexPublicationRequest]) (*connect.Response[indexv1.ResolveIndexPublicationResponse], error) {
	verdict, err := h.service.Resolve(ctx, req.Msg.GetWorkerId(), req.Msg.GetPublicationId(), req.Msg.GetLeaseToken())
	if err != nil {
		return nil, mapError(err)
	}
	response := &indexv1.ResolveIndexPublicationResponse{
		Publication: publicationProto(verdict.Publication, ""),
	}
	switch verdict.Verdict {
	case "resolved":
		response.Verdict = indexv1.ResolveIndexPublicationResponse_VERDICT_RESOLVED
		response.Source = sourceProto(verdict.Source)
	case "tombstoned":
		response.Verdict = indexv1.ResolveIndexPublicationResponse_VERDICT_TOMBSTONED
	case "corrupt":
		response.Verdict = indexv1.ResolveIndexPublicationResponse_VERDICT_CORRUPT
	case "unsupported":
		// The wire enum has no unsupported verdict: an unsupported source is
		// a permanent degraded outcome the consumer records on complete.
		response.Verdict = indexv1.ResolveIndexPublicationResponse_VERDICT_CORRUPT
	default:
		return nil, connect.NewError(connect.CodeInternal, errors.New("index publication verdict is invalid"))
	}
	return connect.NewResponse(response), nil
}

func (h *Handler) CompleteIndexPublications(ctx context.Context, req *connect.Request[indexv1.CompleteIndexPublicationsRequest]) (*connect.Response[indexv1.CompleteIndexPublicationsResponse], error) {
	entries := make([]indexfeedapp.CompleteEntry, 0, len(req.Msg.GetResults()))
	for _, result := range req.Msg.GetResults() {
		entries = append(entries, indexfeedapp.CompleteEntry{
			PublicationID: result.GetPublicationId(),
			LeaseToken:    result.GetLeaseToken(),
			Outcome:       outcomeString(result.GetOutcome()),
		})
	}
	acked, err := h.service.Complete(ctx, indexfeedapp.CompleteInput{WorkerID: req.Msg.GetWorkerId(), Results: entries})
	if err != nil {
		return nil, mapError(err)
	}
	results := make([]*indexv1.IndexPublicationAck, 0, len(acked))
	for _, result := range acked {
		results = append(results, &indexv1.IndexPublicationAck{PublicationId: result.PublicationID, Acked: result.Acked})
	}
	return connect.NewResponse(&indexv1.CompleteIndexPublicationsResponse{Results: results}), nil
}

func publicationProto(publication domain.Publication, leaseToken string) *indexv1.IndexPublication {
	wire := &indexv1.IndexPublication{
		PublicationId: publication.ID,
		Operation:     operationProto(publication.Operation),
		OwnerUserId:   publication.OwnerUserID,
		ProjectId:     publication.ProjectID,
		SourceType:    publication.SourceType,
		SourceId:      publication.SourceID,
		ArtifactType:  publication.ArtifactType,
		Digest:        publication.Digest,
		OccurredAt:    timestamppb.New(publication.OccurredAt),
		LeaseToken:    leaseToken,
	}
	return wire
}

func sourceProto(source *ports.VerifiedSource) *indexv1.ResolveIndexPublicationResponse_ReviewArtifactSource {
	if source == nil {
		return nil
	}
	return &indexv1.ResolveIndexPublicationResponse_ReviewArtifactSource{
		OwnerUserId:  source.OwnerUserID,
		ProjectId:    source.ProjectID,
		ArtifactId:   source.ArtifactID,
		SourceTaskId: source.SourceTaskID,
		ArtifactType: source.ArtifactType,
		Digest:       source.Digest,
		Title:        source.Title,
		Content:      source.Content,
		CreatedAt:    timestamppb.New(source.CreatedAt),
	}
}

func operationProto(operation string) indexv1.IndexPublicationOperation {
	switch operation {
	case domain.OperationReviewArtifactUpsert:
		return indexv1.IndexPublicationOperation_INDEX_PUBLICATION_OPERATION_REVIEW_ARTIFACT_UPSERT
	case domain.OperationProjectTombstone:
		return indexv1.IndexPublicationOperation_INDEX_PUBLICATION_OPERATION_PROJECT_TOMBSTONE
	default:
		return indexv1.IndexPublicationOperation_INDEX_PUBLICATION_OPERATION_UNSPECIFIED
	}
}

func outcomeString(outcome indexv1.IndexPublicationOutcome) string {
	switch outcome {
	case indexv1.IndexPublicationOutcome_INDEX_PUBLICATION_OUTCOME_COMPLETED:
		return domain.OutcomeCompleted
	case indexv1.IndexPublicationOutcome_INDEX_PUBLICATION_OUTCOME_TOMBSTONED:
		return domain.OutcomeTombstoned
	case indexv1.IndexPublicationOutcome_INDEX_PUBLICATION_OUTCOME_UNSUPPORTED:
		return domain.OutcomeUnsupported
	case indexv1.IndexPublicationOutcome_INDEX_PUBLICATION_OUTCOME_CORRUPT:
		return domain.OutcomeCorrupt
	default:
		return ""
	}
}

// mapError converts feed verdicts to sanitized Connect codes. Stale leases
// are ordinary control flow for a crashed or slow consumer, not a server
// fault; transient outages stay retryable; corruption never leaks detail.
func mapError(err error) error {
	switch {
	case errors.Is(err, indexfeedapp.ErrInvalidClaim), errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("index feed request is invalid"))
	case errors.Is(err, domain.ErrLeaseStale):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("index publication claim is not live"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("index publication is not available"))
	case errors.Is(err, ports.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("index feed source is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("index feed operation failed"))
	}
}

func (h *Handler) ReconcileIndexSources(ctx context.Context, req *connect.Request[indexv1.ReconcileIndexSourcesRequest]) (*connect.Response[indexv1.ReconcileIndexSourcesResponse], error) {
	page, err := h.service.ReconcileSources(ctx, req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, mapError(err)
	}
	sources := make([]*indexv1.ReconcileIndexSource, 0, len(page.Sources))
	for _, source := range page.Sources {
		sources = append(sources, &indexv1.ReconcileIndexSource{
			OwnerUserId:  source.OwnerUserID,
			ProjectId:    source.ProjectID,
			ArtifactId:   source.ArtifactID,
			ArtifactType: source.ArtifactType,
			Digest:       source.Digest,
			CreatedAt:    timestamppb.New(source.CreatedAt),
		})
	}
	return connect.NewResponse(&indexv1.ReconcileIndexSourcesResponse{
		Sources:       sources,
		NextPageToken: page.NextToken,
	}), nil
}

func (h *Handler) ReconcileArchivedProjects(ctx context.Context, req *connect.Request[indexv1.ReconcileArchivedProjectsRequest]) (*connect.Response[indexv1.ReconcileArchivedProjectsResponse], error) {
	page, err := h.service.ReconcileArchivedProjects(ctx, req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, mapError(err)
	}
	projects := make([]*indexv1.ReconcileArchivedProject, 0, len(page.Projects))
	for _, project := range page.Projects {
		projects = append(projects, &indexv1.ReconcileArchivedProject{
			OwnerUserId: project.OwnerUserID,
			ProjectId:   project.ProjectID,
			ArchivedAt:  timestamppb.New(project.ArchivedAt),
		})
	}
	return connect.NewResponse(&indexv1.ReconcileArchivedProjectsResponse{
		Projects:      projects,
		NextPageToken: page.NextToken,
	}), nil
}

func (h *Handler) ResolveIndexSourceContent(ctx context.Context, req *connect.Request[indexv1.ResolveIndexSourceContentRequest]) (*connect.Response[indexv1.ResolveIndexSourceContentResponse], error) {
	source, err := h.service.ResolveSourceContent(ctx, req.Msg.GetOwnerUserId(), req.Msg.GetProjectId(), req.Msg.GetArtifactId(), req.Msg.GetExpectedDigest())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&indexv1.ResolveIndexSourceContentResponse{
		ArtifactType: source.ArtifactType,
		Digest:       source.Digest,
		Title:        source.Title,
		Content:      source.Content,
		CreatedAt:    timestamppb.New(source.CreatedAt),
	}), nil
}

func (h *Handler) CountPendingPublications(ctx context.Context, _ *connect.Request[indexv1.CountPendingPublicationsRequest]) (*connect.Response[indexv1.CountPendingPublicationsResponse], error) {
	pending, err := h.service.CountPending(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&indexv1.CountPendingPublicationsResponse{Pending: pending}), nil
}
