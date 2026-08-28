package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
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
		metadata.GetCreatedAt() != nil || metadata.GetTotalSizeBytes() != 0 || metadata.GetFileCount() != 0 {
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

// mapError converts domain failures to Connect codes with sanitized messages:
// no SQL, constraint names, paths, or bundle content. A temporarily
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
	case errors.Is(err, ports.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("artifact store is temporarily unavailable"))
	case application.IsReferenceCorrupt(err):
		return connect.NewError(connect.CodeInternal, errors.New(application.SanitizeMessage))
	default:
		return connect.NewError(connect.CodeInternal, errors.New(application.SanitizeMessage))
	}
}

// ArtifactToProto maps the metadata fact to the public Artifact message. It
// never carries file bytes or a filesystem path.
func ArtifactToProto(artifact domain.Artifact) *artifactv1.Artifact {
	created := artifact.CreatedAt
	return &artifactv1.Artifact{
		Id: artifact.ID, Type: artifact.Type, Title: artifact.Title,
		MediaType: artifact.MediaType, ContentRef: artifact.ContentRef,
		Digest: artifact.Digest, TotalSizeBytes: artifact.TotalSizeBytes,
		FileCount: int32(artifact.FileCount), CreatedAt: timestamppb.New(created),
	}
}
