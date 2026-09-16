// Surface continuity transport (ADR-0031): the public Connect
// SurfaceContinuityService. Owner and device identities arrive only from the
// gateway-injected context; error messages are fixed and sanitized — the one
// honest exception is the stopped-workload attach verdict, which carries the
// server-derived state so the caller can decide to start a new session.
package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/application"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// ContinuityHandler serves the surface continuity RPCs.
type ContinuityHandler struct {
	service *application.ContinuityService
}

// NewContinuityHandler wires the transport into a real Connect handler.
func NewContinuityHandler(service *application.ContinuityService) (string, http.Handler) {
	return surfacev1connect.NewSurfaceContinuityServiceHandler(&ContinuityHandler{service: service})
}

func (h *ContinuityHandler) ListProjectSurfaces(ctx context.Context, req *connect.Request[surfacev1.ListProjectSurfacesRequest]) (*connect.Response[surfacev1.ListProjectSurfacesResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	summaries, err := h.service.ListProjectSurfaces(ctx, id.UserID, req.Msg.GetProjectId())
	if err != nil {
		return nil, continuityError(err)
	}
	views := make([]*surfacev1.SurfaceWorkloadView, 0, len(summaries))
	for _, summary := range summaries {
		views = append(views, workloadViewProto(summary))
	}
	return connect.NewResponse(&surfacev1.ListProjectSurfacesResponse{Workloads: views}), nil
}

func (h *ContinuityHandler) AttachSurface(ctx context.Context, req *connect.Request[surfacev1.AttachSurfaceRequest]) (*connect.Response[surfacev1.AttachSurfaceResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	result, err := h.service.AttachSurface(ctx, id.UserID, id.DeviceID, req.Msg.GetWorkloadId(), req.Msg.GetIdempotencyKey())
	if err != nil {
		if errors.Is(err, domain.ErrWorkloadNotRunning) {
			// The true stopped state is the verdict: attach never starts a
			// second program.
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("surface workload is not running (state: %s); start a new session", result.Workload.State))
		}
		return nil, continuityError(err)
	}
	return connect.NewResponse(&surfacev1.AttachSurfaceResponse{
		Session:    continuitySessionProto(result.Workload),
		Attachment: attachmentProto(result.Attachment),
	}), nil
}

func (h *ContinuityHandler) DetachSurface(ctx context.Context, req *connect.Request[surfacev1.DetachSurfaceRequest]) (*connect.Response[surfacev1.DetachSurfaceResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if err := h.service.DetachSurface(ctx, id.UserID, id.DeviceID, req.Msg.GetSurfaceSessionId()); err != nil {
		return nil, continuityError(err)
	}
	return connect.NewResponse(&surfacev1.DetachSurfaceResponse{}), nil
}

func (h *ContinuityHandler) RequestSurfaceControl(ctx context.Context, req *connect.Request[surfacev1.RequestSurfaceControlRequest]) (*connect.Response[surfacev1.RequestSurfaceControlResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	result, err := h.service.RequestSurfaceControl(ctx, id.UserID, id.DeviceID, req.Msg.GetSurfaceSessionId())
	if err != nil {
		return nil, continuityError(err)
	}
	return connect.NewResponse(&surfacev1.RequestSurfaceControlResponse{Attachment: attachmentProto(result.Attachment)}), nil
}

func (h *ContinuityHandler) GetSurfaceControl(ctx context.Context, req *connect.Request[surfacev1.GetSurfaceControlRequest]) (*connect.Response[surfacev1.GetSurfaceControlResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	facts, err := h.service.GetSurfaceControl(ctx, id.UserID, req.Msg.GetWorkloadId())
	if err != nil {
		return nil, continuityError(err)
	}
	response := &surfacev1.GetSurfaceControlResponse{WorkloadRunning: facts.Running}
	if facts.Found {
		response.ControlGeneration = facts.Lease.ControlGeneration
		response.ControllerDeviceId = facts.Lease.ControllerDeviceID
		response.ControllerAttachmentId = facts.Lease.ControllerAttachmentID
		response.ControlExpiresAt = timestamppb.New(facts.Lease.ExpiresAt)
	}
	return connect.NewResponse(response), nil
}

func (h *ContinuityHandler) StopSurfaceWorkload(ctx context.Context, req *connect.Request[surfacev1.StopSurfaceWorkloadRequest]) (*connect.Response[surfacev1.StopSurfaceWorkloadResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	summary, err := h.service.StopSurfaceWorkload(ctx, id.UserID, req.Msg.GetWorkloadId(), req.Msg.GetActionKey())
	if err != nil {
		return nil, continuityError(err)
	}
	return connect.NewResponse(&surfacev1.StopSurfaceWorkloadResponse{Workload: workloadViewProto(summary)}), nil
}

func (h *ContinuityHandler) RestartSurfaceWorkload(ctx context.Context, req *connect.Request[surfacev1.RestartSurfaceWorkloadRequest]) (*connect.Response[surfacev1.RestartSurfaceWorkloadResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	summary, err := h.service.RestartSurfaceWorkload(ctx, id.UserID, req.Msg.GetWorkloadId(), req.Msg.GetActionKey())
	if err != nil {
		return nil, continuityError(err)
	}
	return connect.NewResponse(&surfacev1.RestartSurfaceWorkloadResponse{Workload: workloadViewProto(summary)}), nil
}

// workloadViewProto projects the application summary. Terminal workloads
// carry the session's expiry as the honest stopped time: the PTY/native close
// paths stamp it at reclaim.
func workloadViewProto(summary application.ContinuitySummary) *surfacev1.SurfaceWorkloadView {
	view := &surfacev1.SurfaceWorkloadView{
		WorkloadId:  summary.Workload.WorkloadID,
		ProjectId:   summary.Workload.ProjectID,
		Renderer:    continuityRendererProto(summary.Workload.Kind),
		DisplayName: workloadDisplayName(summary.Workload.Kind),
		// System tool workloads serve no installed app association; the
		// fields stay empty rather than inventing one.
		Generation:      summary.Generation,
		State:           summary.Workload.State,
		AttachmentCount: summary.AttachmentCount,
		StartedAt:       timestamppb.New(summary.Workload.CreatedAt),
		Policy:          workloadPolicyProto(summary),
	}
	if summary.Workload.Terminal {
		view.StoppedAt = timestamppb.New(summary.Workload.ExpiresAt)
	}
	return view
}

// workloadDisplayName labels the system tool surface families; installed app
// workloads will carry their manifest name instead.
func workloadDisplayName(kind ports.WorkloadKind) string {
	if kind == ports.WorkloadKindNative {
		return "Native display"
	}
	return "Terminal"
}

func workloadPolicyProto(summary application.ContinuitySummary) *surfacev1.WorkloadPolicy {
	return &surfacev1.WorkloadPolicy{
		// Honest bounded policy: sessions survive window close and detach
		// within the existing 30-minute ceiling; expiry stops the program
		// and is reported as the true reason. Idle stop stays unconfigured
		// (0) — the absolute session ceiling is the only policy today.
		Persistent:       true,
		KeepAliveSeconds: summary.KeepAliveSeconds,
	}
}

// continuityRendererProto maps the interactive workload kinds onto the
// renderer vocabulary. Native display sessions are remote-native; PTY
// sessions have no renderer concept and stay unspecified rather than
// borrowing a wrong value.
func continuityRendererProto(kind ports.WorkloadKind) surfacev1.SurfaceRenderer {
	if kind == ports.WorkloadKindNative {
		return surfacev1.SurfaceRenderer_SURFACE_RENDERER_REMOTE_NATIVE
	}
	return surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED
}

// continuitySessionProto projects the session facts a re-attaching device
// needs: the id to drive (PtySessionService for terminals,
// ConnectNativeSession for displays), the renderer, and the bounded session
// window. No bridge credential exists for system tool surfaces.
func continuitySessionProto(workload ports.InteractiveWorkload) *surfacev1.SurfaceSession {
	return &surfacev1.SurfaceSession{
		Id:        workload.WorkloadID,
		ProjectId: workload.ProjectID,
		Renderer:  continuityRendererProto(workload.Kind),
		// Terminals resize through the PTY service; native display sessions
		// have a fixed capture geometry in this phase.
		Resize:    workload.Kind == ports.WorkloadKindPty,
		CreatedAt: timestamppb.New(workload.CreatedAt),
		ExpiresAt: timestamppb.New(workload.ExpiresAt),
	}
}

func attachmentProto(attachment domain.SurfaceAttachment) *surfacev1.SurfaceAttachment {
	proto := &surfacev1.SurfaceAttachment{
		Id:                attachment.ID,
		WorkloadId:        attachment.WorkloadID,
		SurfaceSessionId:  attachment.SurfaceSessionID,
		DeviceId:          attachment.DeviceID,
		ControlGeneration: attachment.ControlGeneration,
		Controls:          attachment.Controls,
		State:             string(attachment.State),
		AttachedAt:        timestamppb.New(attachment.AttachedAt),
	}
	if attachment.ControlExpiresAt != nil {
		proto.ControlExpiresAt = timestamppb.New(*attachment.ControlExpiresAt)
	}
	if attachment.DetachedAt != nil {
		proto.DetachedAt = timestamppb.New(*attachment.DetachedAt)
	}
	return proto
}

// continuityError converts continuity failures to sanitized Connect codes.
func continuityError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("surface continuity request is invalid"))
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, ports.ErrContinuityNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("surface continuity target is not available"))
	case errors.Is(err, domain.ErrWorkloadNotRestartable):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("session workloads are not restartable; start a new session"))
	case errors.Is(err, domain.ErrControlDenied), errors.Is(err, domain.ErrControlExpired), errors.Is(err, ports.ErrContinuityDenied):
		return connect.NewError(connect.CodePermissionDenied, errors.New("surface control is held by another device"))
	case errors.Is(err, ports.ErrContinuityStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("surface continuity is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("surface continuity operation failed"))
	}
}
