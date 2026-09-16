package transport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/application"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handler struct {
	service *application.Service
	now     func() time.Time
}

// NewWorkspaceHostHandler mounts the private workspace host service. The
// runtime host exposes it on its internal listener only; it is never on the
// gateway allowlist.
func NewWorkspaceHostHandler(service *application.Service, now func() time.Time) (string, http.Handler) {
	if now == nil {
		now = time.Now
	}
	return workloadv1connect.NewWorkspaceHostServiceHandler(&Handler{service: service, now: now})
}

func (h *Handler) DescribeWorkspaceSources(ctx context.Context, req *connect.Request[workloadv1.DescribeWorkspaceSourcesRequest]) (*connect.Response[workloadv1.DescribeWorkspaceSourcesResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	sources := h.service.Sources(owner.UserID)
	out := make([]*workloadv1.WorkspaceSource, 0, len(sources))
	for _, source := range sources {
		out = append(out, &workloadv1.WorkspaceSource{
			Id: source.ID, ProjectId: source.ProjectID, Kind: source.Kind, DisplayName: source.DisplayName,
			ReadOnly: source.ReadOnly, RegisteredAt: timestamppb.New(source.Registered),
		})
	}
	return connect.NewResponse(&workloadv1.DescribeWorkspaceSourcesResponse{Sources: out}), nil
}

func (h *Handler) PrepareWorkspace(ctx context.Context, req *connect.Request[workloadv1.PrepareWorkspaceRequest]) (*connect.Response[workloadv1.PrepareWorkspaceResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	environment, err := h.service.Prepare(ctx, owner.UserID, req.Msg.GetProjectId(), req.Msg.GetConsumer(), h.now().UTC())
	if err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&workloadv1.PrepareWorkspaceResponse{
		EnvironmentId: environment.ID, WorkspaceSourceId: environment.SourceID,
		ReadOnly:   environment.ReadOnly,
		PreparedAt: timestamppb.New(environment.PreparedAt), ExpiresAt: timestamppb.New(environment.ExpiresAt),
	}), nil
}

func workspaceError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, application.ErrNoWorkspace):
		code = connect.CodeNotFound
	case errors.Is(err, application.ErrInvalidScope):
		code = connect.CodeInvalidArgument
	}
	return connect.NewError(code, errors.New("workspace host request failed"))
}
