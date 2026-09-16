package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	releasev1 "github.com/yangtao121/workos/gen/go/workos/release/v1"
	releasev1connect "github.com/yangtao121/workos/gen/go/workos/release/v1/releasev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/reliability/application"
	"github.com/yangtao121/workos/internal/reliability/domain"
)

type ReleaseHandler struct {
	service *application.ReleaseService
}

func NewReleaseConnectHandler(service *application.ReleaseService) (string, http.Handler) {
	return releasev1connect.NewReleaseServiceHandler(&ReleaseHandler{service: service}, connect.WithReadMaxBytes(4096))
}

func (h *ReleaseHandler) GetReleaseStatus(ctx context.Context, req *connect.Request[releasev1.GetReleaseStatusRequest]) (*connect.Response[releasev1.GetReleaseStatusResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	status, err := h.service.GetStatus(ctx, id.UserID, req.Msg.GetProjectId(), req.Msg.GetInstallationId())
	if err != nil {
		if errors.Is(err, application.ErrDeploymentCandidateRequired) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid release identity"))
		}
		if errors.Is(err, domain.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("release status is not available"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("release status is unavailable"))
	}
	return connect.NewResponse(&releasev1.GetReleaseStatusResponse{
		Status: &releasev1.ReleaseStatus{
			InstallationId:          status.InstallationID,
			AppId:                   status.AppID,
			State:                   status.State,
			CandidateVersion:        status.CandidateVersion,
			CandidateArtifactDigest: status.CandidateArtifactDigest,
			BaseVersion:             status.BaseVersion,
			BaseArtifactDigest:      status.BaseArtifactDigest,
			FailureCategory:         status.FailureCategory,
			UpdatedAt:               status.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			Revision:                status.Revision,
		},
	}), nil
}
