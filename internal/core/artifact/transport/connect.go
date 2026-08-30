package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	artifactv1connect "github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/internal/core/artifact/application"
	"github.com/yangtao121/workos/internal/core/artifact/domain"
	"github.com/yangtao121/workos/internal/core/artifact/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// MaxRequestBytes bounds every ArtifactService request message before the
// Connect stack decodes it. The legal web bundle payload is at most 2 MiB of
// file bytes; protojson inflates bytes 4/3 through base64 to ~2.8 MiB before
// field names, paths, and JSON punctuation. 4 MiB (4,194,304 bytes) covers
// that with headroom while staying a small explicit constant — the library
// default is unlimited. Application-level bundle limits stay in place.
const MaxRequestBytes = 4 * 1024 * 1024

type Handler struct{ service *application.Service }

func New(service *application.Service) *Handler { return &Handler{service: service} }

// NewConnectHandler wires the transport into a real Connect handler with the
// bounded-read configuration. Composition roots and tests must use this
// constructor so the read limit is identical in production and tests.
func NewConnectHandler(service *application.Service) (string, http.Handler) {
	return artifactv1connect.NewArtifactServiceHandler(
		New(service),
		connect.WithReadMaxBytes(MaxRequestBytes),
	)
}

func (h *Handler) CreateArtifact(ctx context.Context, req *connect.Request[artifactv1.CreateArtifactRequest]) (*connect.Response[artifactv1.CreateArtifactResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	bundleInput := req.Msg.GetWebBundle()
	if bundleInput == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("only web bundle artifacts are supported"))
	}
	// Server-owned metadata must be empty: the client may submit the title
	// only, everything else is decided and persisted by the server.
	metadata := req.Msg.GetArtifact()
	if metadata == nil {
		metadata = &artifactv1.Artifact{}
	}
	if metadata.GetId() != "" || metadata.GetProjectId() != "" || metadata.GetType() != "" ||
		metadata.GetMediaType() != "" || metadata.GetContentRef() != "" || metadata.GetDigest() != "" ||
		metadata.GetCreatedAt() != nil || metadata.GetTotalSizeBytes() != 0 || metadata.GetFileCount() != 0 ||
		metadata.GetSourceTaskId() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("artifact metadata is server-owned; only the title may be set"))
	}
	files := make([]domain.BundleFileInput, 0, len(bundleInput.GetFiles()))
	for _, file := range bundleInput.GetFiles() {
		files = append(files, domain.BundleFileInput{Path: file.GetPath(), Content: file.GetContent()})
	}
	artifact, err := h.service.CreateWebBundle(ctx, id.UserID, req.Msg.GetIdempotencyKey(), metadata.GetTitle(), bundleInput.GetEntrypoint(), files)
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&artifactv1.CreateArtifactResponse{Artifact: ArtifactToProto(artifact)}), nil
}

func (h *Handler) GetArtifact(ctx context.Context, req *connect.Request[artifactv1.GetArtifactRequest]) (*connect.Response[artifactv1.GetArtifactResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if err := rejectOversizeRead(req.Msg); err != nil {
		return nil, err
	}
	artifact, err := h.service.Get(ctx, id.UserID, req.Msg.GetArtifactId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&artifactv1.GetArtifactResponse{Artifact: ArtifactToProto(artifact)}), nil
}

func (h *Handler) ListArtifacts(ctx context.Context, req *connect.Request[artifactv1.ListArtifactsRequest]) (*connect.Response[artifactv1.ListArtifactsResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if err := rejectOversizeRead(req.Msg); err != nil {
		return nil, err
	}
	pageSize, pageToken := 0, ""
	if req.Msg.GetPage() != nil {
		pageSize, pageToken = int(req.Msg.GetPage().GetPageSize()), req.Msg.GetPage().GetPageToken()
	}
	result, err := h.service.List(ctx, id.UserID, req.Msg.GetProjectId(), pageToken, pageSize)
	if err != nil {
		return nil, mapError(err)
	}
	artifacts := make([]*artifactv1.Artifact, 0, len(result.Items))
	for _, item := range result.Items {
		artifacts = append(artifacts, ArtifactToProto(item))
	}
	return connect.NewResponse(&artifactv1.ListArtifactsResponse{
		Artifacts: artifacts, Page: &commonv1.PageResponse{NextPageToken: result.NextToken},
	}), nil
}

// MaxReadRequestBytes bounds the review read/list/get requests. All their
// fields are bounded-grammar identifiers, so the service-level Create budget
// (MaxRequestBytes, needed by the web bundle upload) stays, and these
// handlers enforce the tighter contract directly on the decoded message
// before any application code runs.
const MaxReadRequestBytes = 32 * 1024

// rejectOversizeRead enforces the review read budget. A decoded message
// beyond the fixed bound is ResourceExhausted before any business code.
func rejectOversizeRead(msg proto.Message) error {
	if proto.Size(msg) > MaxReadRequestBytes {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("artifact read request is too large"))
	}
	return nil
}

func (h *Handler) GetReviewArtifact(ctx context.Context, req *connect.Request[artifactv1.GetReviewArtifactRequest]) (*connect.Response[artifactv1.GetReviewArtifactResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if err := rejectOversizeRead(req.Msg); err != nil {
		return nil, err
	}
	fact, content, err := h.service.GetReview(ctx, id.UserID, req.Msg.GetArtifactId())
	if err != nil {
		return nil, mapError(err)
	}
	response := &artifactv1.GetReviewArtifactResponse{Artifact: ArtifactToProto(metadataProjection(fact))}
	switch fact.Type {
	case domain.TypeMarkdown:
		response.Content = &artifactv1.ReviewArtifactContent{
			Content: &artifactv1.ReviewArtifactContent_Markdown_{
				Markdown: &artifactv1.ReviewArtifactContent_Markdown{
					MediaType: domain.MediaTypeMarkdown, Content: content.Content,
				},
			},
		}
	case domain.TypeUnifiedDiff:
		response.Content = &artifactv1.ReviewArtifactContent{
			Content: &artifactv1.ReviewArtifactContent_UnifiedDiff_{
				UnifiedDiff: &artifactv1.ReviewArtifactContent_UnifiedDiff{
					MediaType: domain.MediaTypeUnifiedDiff, Content: content.Content,
				},
			},
		}
	default:
		// A stored row of an unimplemented subtype can never be served as
		// review content; web bundle bytes stay private and the verdict is
		// the fixed unsupported semantic.
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("artifact type is not reviewable"))
	}
	return connect.NewResponse(response), nil
}

// metadataProjection adapts the review fact to the shared metadata record
// for the sanitized public projection.
func metadataProjection(fact domain.ReviewArtifact) domain.Artifact {
	return domain.Artifact{
		ID: fact.ID, OwnerUserID: fact.OwnerUserID, Type: fact.Type, Title: fact.Title,
		MediaType: fact.MediaType, Digest: fact.Digest, FileCount: 1,
		TotalSizeBytes: int64(fact.ByteCount), CreatedAt: fact.CreatedAt,
		ProjectID: fact.ProjectID, SourceTaskID: fact.SourceTask,
	}
}

// mapError converts domain failures to Connect codes with sanitized messages:
// no SQL, constraint names, paths, or artifact content. A temporarily
// unreachable store is Unavailable; unknown and invariant failures stay
// Internal.
func mapError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("artifact request is invalid"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("artifact is not available for this owner"))
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return connect.NewError(connect.CodeAborted, errors.New("idempotency key was already used for a different request"))
	case errors.Is(err, domain.ErrUnsupported):
		return connect.NewError(connect.CodeUnimplemented, errors.New("artifact type is not supported"))
	case errors.Is(err, domain.ErrDigestMismatch):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("artifact digest does not match the referenced artifact"))
	case errors.Is(err, domain.ErrCorrupt):
		return connect.NewError(connect.CodeInternal, errors.New(application.SanitizeMessage))
	case errors.Is(err, ports.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("artifact store is temporarily unavailable"))
	case application.IsReferenceCorrupt(err):
		return connect.NewError(connect.CodeInternal, errors.New(application.SanitizeMessage))
	default:
		return connect.NewError(connect.CodeInternal, errors.New(application.SanitizeMessage))
	}
}

// ArtifactToProto maps the metadata fact to the public Artifact message. It
// never carries content bytes or a filesystem path. Review artifacts carry
// their project/source-task provenance; web bundles leave both empty.
func ArtifactToProto(artifact domain.Artifact) *artifactv1.Artifact {
	created := artifact.CreatedAt
	return &artifactv1.Artifact{
		Id: artifact.ID, ProjectId: artifact.ProjectID, Type: artifact.Type, Title: artifact.Title,
		MediaType: artifact.MediaType, ContentRef: artifact.ContentRef,
		Digest: artifact.Digest, TotalSizeBytes: artifact.TotalSizeBytes,
		FileCount: int32(artifact.FileCount), CreatedAt: timestamppb.New(created),
		SourceTaskId: artifact.SourceTaskID,
	}
}
