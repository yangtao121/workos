// Preview transport (ADR-0030 B08): the public Connect
// WorkspacePreviewService plus the same-origin /previews/<id>/ serving
// route. Owner and device identities arrive only from the gateway-injected
// context; error messages are fixed and sanitized.
package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/previewhost/application"
	"github.com/yangtao121/workos/internal/runtime/previewhost/domain"
	"github.com/yangtao121/workos/internal/runtime/previewhost/ports"
)

// PreviewURL is the same-origin relative serving path of one preview.
func PreviewURL(previewID, token string) string {
	return "/previews/" + previewID + "/" + token + "/"
}

// PreviewHandler serves the workspace preview RPCs.
type PreviewHandler struct {
	service *application.Service
}

// NewPreviewHandler wires the transport into a real Connect handler.
func NewPreviewHandler(service *application.Service) (string, http.Handler) {
	return surfacev1connect.NewWorkspacePreviewServiceHandler(&PreviewHandler{service: service})
}

func (h *PreviewHandler) StartWorkspacePreview(ctx context.Context, req *connect.Request[surfacev1.StartWorkspacePreviewRequest]) (*connect.Response[surfacev1.StartWorkspacePreviewResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	record, err := h.service.Start(ctx, id.UserID, req.Msg.GetProjectId(), req.Msg.GetIdempotencyKey(), req.Msg.GetCommand(), req.Msg.GetPort())
	if err != nil {
		if errors.Is(err, domain.ErrNoWorkspace) {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("no workspace bound to this project on this runtime host"))
		}
		return nil, previewError(err)
	}
	return connect.NewResponse(&surfacev1.StartWorkspacePreviewResponse{Preview: previewProto(record)}), nil
}

func (h *PreviewHandler) GetWorkspacePreview(ctx context.Context, req *connect.Request[surfacev1.GetWorkspacePreviewRequest]) (*connect.Response[surfacev1.GetWorkspacePreviewResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	record, err := h.service.Get(ctx, id.UserID, req.Msg.GetPreviewId())
	if err != nil {
		return nil, previewError(err)
	}
	return connect.NewResponse(&surfacev1.GetWorkspacePreviewResponse{Preview: previewProto(record)}), nil
}

func (h *PreviewHandler) ListProjectWorkspacePreviews(ctx context.Context, req *connect.Request[surfacev1.ListProjectWorkspacePreviewsRequest]) (*connect.Response[surfacev1.ListProjectWorkspacePreviewsResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	records, err := h.service.List(ctx, id.UserID, req.Msg.GetProjectId())
	if err != nil {
		return nil, previewError(err)
	}
	previews := make([]*surfacev1.WorkspacePreview, 0, len(records))
	for _, record := range records {
		previews = append(previews, previewProto(record))
	}
	return connect.NewResponse(&surfacev1.ListProjectWorkspacePreviewsResponse{Previews: previews}), nil
}

func (h *PreviewHandler) StopWorkspacePreview(ctx context.Context, req *connect.Request[surfacev1.StopWorkspacePreviewRequest]) (*connect.Response[surfacev1.StopWorkspacePreviewResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if err := h.service.Stop(ctx, id.UserID, req.Msg.GetPreviewId(), req.Msg.GetActionKey()); err != nil {
		return nil, previewError(err)
	}
	return connect.NewResponse(&surfacev1.StopWorkspacePreviewResponse{}), nil
}

func previewProto(record ports.PreviewRecord) *surfacev1.WorkspacePreview {
	return &surfacev1.WorkspacePreview{
		Id: record.PreviewID, OwnerUserId: record.OwnerUserID, ProjectId: record.ProjectID,
		WorkspaceSourceId: record.WorkspaceSourceID, ReadOnly: record.ReadOnly,
		Url: PreviewURL(record.PreviewID, record.AccessToken), State: record.State, Generation: record.Generation, Command: record.Command, Port: record.Port,
		CreatedAt: timestamppb.New(record.CreatedAt), ExpiresAt: timestamppb.New(record.ExpiresAt),
	}
}

// previewError converts domain failures to Connect codes with sanitized
// messages. Unknown/foreign/stopped/expired previews share one NotFound so
// the verdict never doubles as an existence probe.
func previewError(err error) error {
	switch {
	case errors.Is(err, domain.ErrConflict):
		return connect.NewError(connect.CodeAborted, errors.New("preview intent conflicts with recorded request"))
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("workspace preview request is invalid"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("workspace preview is not available for this owner"))
	case errors.Is(err, domain.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("workspace preview store is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("workspace preview operation failed"))
	}
}

func (h *PreviewHandler) RestartWorkspacePreview(ctx context.Context, req *connect.Request[surfacev1.RestartWorkspacePreviewRequest]) (*connect.Response[surfacev1.RestartWorkspacePreviewResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	record, err := h.service.Restart(ctx, id.UserID, req.Msg.GetPreviewId(), req.Msg.GetActionKey())
	if err != nil {
		return nil, previewError(err)
	}
	return connect.NewResponse(&surfacev1.RestartWorkspacePreviewResponse{Preview: previewProto(record)}), nil
}
