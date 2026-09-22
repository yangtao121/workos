package transport

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	desktopv1 "github.com/yangtao121/workos/gen/go/workos/desktop/v1"
	"github.com/yangtao121/workos/gen/go/workos/desktop/v1/desktopv1connect"
	"github.com/yangtao121/workos/internal/core/desktop/application"
	"github.com/yangtao121/workos/internal/core/desktop/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
)

const MaxRequestBytes = 32 * 1024
const WatchMaxLifetime = 2 * time.Minute
const WatchHeartbeat = 15 * time.Second
const WatchPollInterval = 500 * time.Millisecond
const WatchMaxPerOwner = 8
const WatchOwnerBudget = 1024

type Handler struct {
	service                   *application.Service
	mu                        sync.Mutex
	streams                   map[string]int
	poll, heartbeat, lifetime time.Duration
}

func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service, streams: map[string]int{}, poll: WatchPollInterval, heartbeat: WatchHeartbeat, lifetime: WatchMaxLifetime}
}
func NewConnectHandler(service *application.Service) (string, http.Handler) {
	return desktopv1connect.NewDesktopServiceHandler(NewHandler(service), connect.WithReadMaxBytes(MaxRequestBytes))
}
func owner(ctx context.Context) (string, error) {
	id, err := identity.FromContext(ctx)
	if err != nil || !domain.UUID(id.UserID) || !domain.UUID(id.DeviceID) {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("device identity required"))
	}
	return id.UserID, nil
}
func publicError(err error) error {
	code := connect.CodeInternal
	message := "desktop operation failed"
	switch {
	case errors.Is(err, domain.ErrInvalid):
		code = connect.CodeInvalidArgument
		message = "invalid desktop operation"
	case errors.Is(err, domain.ErrNotFound):
		code = connect.CodeNotFound
		message = "desktop target not found"
	case errors.Is(err, domain.ErrConflict):
		code = connect.CodeAborted
		message = "desktop operation key conflict"
	case errors.Is(err, domain.ErrLimit):
		code = connect.CodeResourceExhausted
		message = "desktop limit reached"
	case errors.Is(err, domain.ErrUnavailable):
		code = connect.CodeUnavailable
		message = "desktop dependency unavailable"
	case errors.Is(err, context.Canceled):
		code = connect.CodeCanceled
		message = "desktop request cancelled"
	}
	return connect.NewError(code, errors.New(message))
}
func target(t *desktopv1.DesktopWindowTarget) domain.Target {
	r := domain.Target{Kind: t.GetKind(), ProjectID: t.GetProjectId(), ExpectedWorkloadID: t.GetExpectedWorkloadId(), ExpectedWorkloadGeneration: t.GetExpectedWorkloadGeneration()}
	switch v := t.GetResource().(type) {
	case *desktopv1.DesktopWindowTarget_AppInstanceId:
		r.ResourceKind = "app"
		r.ResourceID = v.AppInstanceId
	case *desktopv1.DesktopWindowTarget_WorkloadId:
		r.ResourceKind = "workload"
		r.ResourceID = v.WorkloadId
	case *desktopv1.DesktopWindowTarget_PreviewId:
		r.ResourceKind = "preview"
		r.ResourceID = v.PreviewId
	case *desktopv1.DesktopWindowTarget_SessionId:
		r.ResourceKind = "session"
		r.ResourceID = v.SessionId
	case *desktopv1.DesktopWindowTarget_ArtifactId:
		r.ResourceKind = "artifact"
		r.ResourceID = v.ArtifactId
	}
	return r
}
func wireTarget(t domain.Target) *desktopv1.DesktopWindowTarget {
	r := &desktopv1.DesktopWindowTarget{Kind: t.Kind, ProjectId: t.ProjectID, ExpectedWorkloadId: t.ExpectedWorkloadID, ExpectedWorkloadGeneration: t.ExpectedWorkloadGeneration}
	switch t.ResourceKind {
	case "app":
		r.Resource = &desktopv1.DesktopWindowTarget_AppInstanceId{AppInstanceId: t.ResourceID}
	case "workload":
		r.Resource = &desktopv1.DesktopWindowTarget_WorkloadId{WorkloadId: t.ResourceID}
	case "preview":
		r.Resource = &desktopv1.DesktopWindowTarget_PreviewId{PreviewId: t.ResourceID}
	case "session":
		r.Resource = &desktopv1.DesktopWindowTarget_SessionId{SessionId: t.ResourceID}
	case "artifact":
		r.Resource = &desktopv1.DesktopWindowTarget_ArtifactId{ArtifactId: t.ResourceID}
	}
	return r
}
func wireState(s domain.State) *desktopv1.DesktopState {
	r := &desktopv1.DesktopState{ActiveProjectId: s.ActiveProjectID, FocusedWindowId: s.FocusedWindowID, Revision: s.Revision}
	for _, w := range s.Windows {
		r.Windows = append(r.Windows, &desktopv1.DesktopWindow{Id: w.ID, Target: wireTarget(w.Target)})
	}
	return r
}
func operation(r *desktopv1.ApplyDesktopOperationRequest) (domain.Operation, error) {
	o := domain.Operation{}
	switch v := r.GetOperation().(type) {
	case *desktopv1.ApplyDesktopOperationRequest_SwitchProject:
		o.Kind = "switch"
		o.ProjectID = v.SwitchProject.GetProjectId()
	case *desktopv1.ApplyDesktopOperationRequest_OpenWindow:
		o.Kind = "open"
		o.Target = target(v.OpenWindow)
	case *desktopv1.ApplyDesktopOperationRequest_CloseWindow:
		o.Kind = "close"
		o.WindowID = v.CloseWindow.GetWindowId()
	case *desktopv1.ApplyDesktopOperationRequest_FocusWindow:
		o.Kind = "focus"
		o.WindowID = v.FocusWindow.GetWindowId()
	case *desktopv1.ApplyDesktopOperationRequest_SelectSession:
		o.Kind = "session"
		o.WindowID = v.SelectSession.GetWindowId()
		o.SessionID = v.SelectSession.GetSessionId()
	case *desktopv1.ApplyDesktopOperationRequest_Initialize:
		o.Kind = "initialize"
		o.ProjectID = v.Initialize.GetActiveProjectId()
		for _, t := range v.Initialize.GetWindows() {
			o.Windows = append(o.Windows, target(t))
		}
	default:
		return o, domain.ErrInvalid
	}
	return o, nil
}
func (h *Handler) GetDesktop(ctx context.Context, _ *connect.Request[desktopv1.GetDesktopRequest]) (*connect.Response[desktopv1.GetDesktopResponse], error) {
	id, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	state, err := h.service.Get(ctx, id)
	if err != nil {
		return nil, publicError(err)
	}
	return connect.NewResponse(&desktopv1.GetDesktopResponse{State: wireState(state)}), nil
}
func (h *Handler) ApplyDesktopOperation(ctx context.Context, r *connect.Request[desktopv1.ApplyDesktopOperationRequest]) (*connect.Response[desktopv1.ApplyDesktopOperationResponse], error) {
	id, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	op, err := operation(r.Msg)
	if err != nil {
		return nil, publicError(err)
	}
	state, err := h.service.Apply(ctx, id, r.Msg.GetIdempotencyKey(), op)
	if err != nil {
		return nil, publicError(err)
	}
	return connect.NewResponse(&desktopv1.ApplyDesktopOperationResponse{State: wireState(state)}), nil
}
func (h *Handler) WatchDesktop(ctx context.Context, r *connect.Request[desktopv1.WatchDesktopRequest], stream *connect.ServerStream[desktopv1.WatchDesktopResponse]) error {
	id, err := owner(ctx)
	if err != nil {
		return err
	}
	after := r.Msg.GetAfterRevision()
	if after < 0 {
		return publicError(domain.ErrInvalid)
	}
	h.mu.Lock()
	if h.streams[id] >= WatchMaxPerOwner || (h.streams[id] == 0 && len(h.streams) >= WatchOwnerBudget) {
		h.mu.Unlock()
		return publicError(domain.ErrLimit)
	}
	h.streams[id]++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.streams[id]--
		if h.streams[id] == 0 {
			delete(h.streams, id)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, h.lifetime)
	defer cancel()
	poll := time.NewTicker(h.poll)
	defer poll.Stop()
	heartbeat := time.NewTicker(h.heartbeat)
	defer heartbeat.Stop()
	var current domain.State
	send := func() error {
		state, events, reset, err := h.service.Changes(ctx, id, after)
		if err != nil {
			return publicError(err)
		}
		current = state
		if reset {
			if err := stream.Send(&desktopv1.WatchDesktopResponse{State: wireState(state), ResetRequired: true}); err != nil {
				return err
			}
			after = state.Revision
			return nil
		}
		for _, event := range events {
			if err := stream.Send(&desktopv1.WatchDesktopResponse{State: wireState(event)}); err != nil {
				return err
			}
			after = event.Revision
		}
		return nil
	}
	if err := send(); err != nil {
		return err
	}
	// Flush even an empty/uninitialized desktop so the caller knows the watch
	// is connected, without advancing its durable revision.
	if err := stream.Send(&desktopv1.WatchDesktopResponse{State: wireState(current), Heartbeat: true}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			if err := send(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		case <-heartbeat.C:
			if err := stream.Send(&desktopv1.WatchDesktopResponse{State: wireState(current), Heartbeat: true}); err != nil {
				return err
			}
		}
	}
}
