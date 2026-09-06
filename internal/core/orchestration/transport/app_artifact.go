package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	artifactv1connect "github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	"github.com/yangtao121/workos/internal/core/artifact/domain"
	"github.com/yangtao121/workos/internal/core/artifact/ports"
	"github.com/yangtao121/workos/internal/core/orchestration"
	"github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/platform/identity"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type AppArtifactHandler struct {
	service *orchestration.AppArtifactService
}

func NewAppArtifactConnectHandler(service *orchestration.AppArtifactService) (string, http.Handler) {
	return artifactv1connect.NewAppArtifactServiceHandler(&AppArtifactHandler{service}, connect.WithReadMaxBytes(64*1024))
}
func appArtifactScope(ctx context.Context, scope *artifactv1.AppArtifactScope) (orchestration.AppArtifactScope, error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return orchestration.AppArtifactScope{}, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}
	return orchestration.AppArtifactScope{OwnerUserID: id.UserID, ProjectID: scope.GetProjectId(), AppInstanceID: scope.GetAppInstanceId(), GrantRevision: scope.GetInstallationGrantRevision()}, nil
}
func (h *AppArtifactHandler) CreateAppReviewArtifact(ctx context.Context, req *connect.Request[artifactv1.CreateAppReviewArtifactRequest]) (*connect.Response[artifactv1.CreateAppReviewArtifactResponse], error) {
	scope, err := appArtifactScope(ctx, req.Msg.GetScope())
	if err != nil {
		return nil, err
	}
	a, err := h.service.Create(ctx, scope, req.Msg.GetIdempotencyKey(), req.Msg.GetType(), req.Msg.GetTitle(), req.Msg.GetContent())
	if err != nil {
		return nil, appArtifactError(err)
	}
	return connect.NewResponse(&artifactv1.CreateAppReviewArtifactResponse{Artifact: appReviewProto(a)}), nil
}
func (h *AppArtifactHandler) OpenAppReviewArtifact(ctx context.Context, req *connect.Request[artifactv1.OpenAppReviewArtifactRequest]) (*connect.Response[artifactv1.OpenAppReviewArtifactResponse], error) {
	scope, err := appArtifactScope(ctx, req.Msg.GetScope())
	if err != nil {
		return nil, err
	}
	a, err := h.service.Open(ctx, scope, req.Msg.GetArtifactId())
	if err != nil {
		return nil, appArtifactError(err)
	}
	return connect.NewResponse(&artifactv1.OpenAppReviewArtifactResponse{Artifact: appReviewProto(a)}), nil
}
func appReviewProto(a domain.ReviewArtifact) *artifactv1.Artifact {
	return &artifactv1.Artifact{Id: a.ID, ProjectId: a.ProjectID, Type: a.Type, Title: a.Title, MediaType: a.MediaType, Digest: a.Digest, CreatedAt: timestamppb.New(a.CreatedAt), FileCount: 1, TotalSizeBytes: int64(a.ByteCount), SourceTaskId: a.SourceTask, SourceAppInstanceId: a.SourceAppInstanceID}
}
func appArtifactError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("artifact input is invalid"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("artifact is unavailable"))
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return connect.NewError(connect.CodeAborted, errors.New("artifact key already used"))
	case errors.Is(err, domain.ErrQuota):
		return connect.NewError(connect.CodeResourceExhausted, errors.New("app artifact limit reached"))
	case errors.Is(err, ports.ErrStoreUnavailable), dbtransient.IsTransient(err):
		return connect.NewError(connect.CodeUnavailable, errors.New("artifact service unavailable"))
	default:
		return mapAppAgentError(err)
	}
}
