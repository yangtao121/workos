package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	"github.com/yangtao121/workos/internal/core/agent/application"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/agent/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// sessionTaskDispatcher admits one session input as a Task run through the
// exact public submission path: provider binding resolution, credential
// snapshots, and policy all happen inside Submitter.Submit before any task
// row exists.
type sessionTaskDispatcher struct {
	submitter Submitter
	service   *application.Service
}

func (d *sessionTaskDispatcher) Dispatch(ctx context.Context, ownerUserID, projectID, providerID, goal, idempotencyKey, sessionID string) (domain.Task, error) {
	// agent_session_id is server-derived linkage (ADR-0030): the public
	// SubmitTask surface rejects it, and only this dispatcher sets it.
	input := &agentv1.AgentTaskInput{
		TargetScope:    &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: projectID}},
		Goal:           goal,
		AgentSessionId: sessionID,
	}
	payload, err := protojson.Marshal(input)
	if err != nil {
		return domain.Task{}, domain.ErrInvalid
	}
	return d.submitter.Submit(ctx, application.SubmitInput{
		OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey, ProjectID: projectID,
		ProviderID: providerID, Payload: payload,
	})
}

func (d *sessionTaskDispatcher) Cancel(ctx context.Context, ownerUserID, taskID, reason string) (domain.Task, error) {
	task, _, err := d.service.Cancel(ctx, ownerUserID, taskID, reason)
	return task, err
}

func (d *sessionTaskDispatcher) Get(ctx context.Context, owner, taskID string) (domain.Task, error) {
	return d.service.Get(ctx, owner, taskID)
}

// SessionSnapshotSource derives the immutable binding facts a new session
// pins: the effective provider (project binding or global default) and the
// project's active workspace binding.
type SessionSnapshotSource interface {
	Snapshot(ctx context.Context, ownerUserID, projectID string) (application.SessionSnapshot, error)
}

type SessionHandler struct {
	agentv1connect.UnimplementedAgentSessionServiceHandler
	service   *application.SessionService
	snapshots SessionSnapshotSource
}

func NewSessionHandler(service *application.SessionService, snapshots SessionSnapshotSource) (string, http.Handler) {
	return agentv1connect.NewAgentSessionServiceHandler(&SessionHandler{service: service, snapshots: snapshots})
}

// NewSessionTaskDispatcher wires the shared admission path into the session
// application service.
func NewSessionTaskDispatcher(submitter Submitter, service *application.Service) ports.SessionTaskDispatcher {
	return &sessionTaskDispatcher{submitter: submitter, service: service}
}

func sessionToProto(session domain.Session) *agentv1.AgentSession {
	state := agentv1.AgentSessionState(agentv1.AgentSessionState_value["AGENT_SESSION_STATE_"+uppercase(string(session.State))])
	out := &agentv1.AgentSession{
		Id: session.ID, OwnerUserId: session.OwnerUserID, ProjectId: session.ProjectID,
		WorkspaceBindingId: session.WorkspaceBindingID, ProviderId: session.ProviderID,
		ProfileId: session.ProfileID, State: state, NativeSessionRef: session.NativeSessionRef,
		ActiveTaskId: session.ActiveTaskID, InputSequence: session.InputSequence,
		LastEventSequence: session.EventSequence,
		CreatedAt:         timestamppb.New(session.CreatedAt), UpdatedAt: timestamppb.New(session.UpdatedAt),
	}
	if session.ClosedAt != nil {
		out.ClosedAt = timestamppb.New(*session.ClosedAt)
	}
	return out
}

func inputStateToProto(state domain.SessionInputState) agentv1.AgentSessionInputState {
	return agentv1.AgentSessionInputState(agentv1.AgentSessionInputState_value["AGENT_SESSION_INPUT_STATE_"+uppercase(string(state))])
}

func inputToProto(input domain.SessionInput) *agentv1.AgentSessionInput {
	return &agentv1.AgentSessionInput{
		Id: input.ID, SessionId: input.SessionID, ClientInputId: input.ClientInputID,
		Text: input.Text, State: inputStateToProto(input.State), TaskId: input.TaskID,
		Sequence: input.Sequence, ResultSummary: input.ResultSummary,
		CreatedAt: timestamppb.New(input.CreatedAt), UpdatedAt: timestamppb.New(input.UpdatedAt),
	}
}

func (h *SessionHandler) CreateSession(ctx context.Context, req *connect.Request[agentv1.CreateSessionRequest]) (*connect.Response[agentv1.CreateSessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if h.snapshots == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("session snapshot source is not configured"))
	}
	snapshot, err := h.snapshots.Snapshot(ctx, owner.UserID, req.Msg.GetProjectId())
	if err != nil {
		return nil, sessionError(err)
	}
	if requested := req.Msg.GetWorkspaceBindingId(); requested != "" && requested != snapshot.WorkspaceBindingID {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("workspace binding is not active for this project"))
	}
	session, err := h.service.Create(ctx, owner.UserID, req.Msg.GetProjectId(), req.Msg.GetIdempotencyKey(), snapshot)
	if err != nil {
		return nil, sessionError(err)
	}
	return connect.NewResponse(&agentv1.CreateSessionResponse{Session: sessionToProto(session)}), nil
}

func (h *SessionHandler) ListSessions(ctx context.Context, req *connect.Request[agentv1.ListSessionsRequest]) (*connect.Response[agentv1.ListSessionsResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	sessions, err := h.service.List(ctx, owner.UserID, req.Msg.GetProjectId(), req.Msg.GetIncludeClosed())
	if err != nil {
		return nil, sessionError(err)
	}
	out := make([]*agentv1.AgentSession, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, sessionToProto(session))
	}
	return connect.NewResponse(&agentv1.ListSessionsResponse{Sessions: out}), nil
}

func (h *SessionHandler) GetSession(ctx context.Context, req *connect.Request[agentv1.GetSessionRequest]) (*connect.Response[agentv1.GetSessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Get(ctx, owner.UserID, req.Msg.GetSessionId())
	if err != nil {
		return nil, sessionError(err)
	}
	return connect.NewResponse(&agentv1.GetSessionResponse{Session: sessionToProto(session)}), nil
}

func (h *SessionHandler) SubmitSessionInput(ctx context.Context, req *connect.Request[agentv1.SubmitSessionInputRequest]) (*connect.Response[agentv1.SubmitSessionInputResponse], error) {
	if req.Msg.GetDirective() != nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("session goal controls unavailable"))
	}
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	input, _, err := h.service.Submit(ctx, owner.UserID, req.Msg.GetSessionId(), req.Msg.GetClientInputId(), req.Msg.GetText())
	if err != nil {
		return nil, sessionError(err)
	}
	return connect.NewResponse(&agentv1.SubmitSessionInputResponse{Input: inputToProto(input)}), nil
}

func (h *SessionHandler) GetSessionInput(ctx context.Context, req *connect.Request[agentv1.GetSessionInputRequest]) (*connect.Response[agentv1.GetSessionInputResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	input, err := h.service.GetInput(ctx, owner.UserID, req.Msg.GetSessionId(), req.Msg.GetClientInputId())
	if err != nil {
		return nil, sessionError(err)
	}
	return connect.NewResponse(&agentv1.GetSessionInputResponse{Input: inputToProto(input)}), nil
}

func (h *SessionHandler) ListSessionInputs(ctx context.Context, req *connect.Request[agentv1.ListSessionInputsRequest]) (*connect.Response[agentv1.ListSessionInputsResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	inputs, err := h.service.ListInputs(ctx, owner.UserID, req.Msg.GetSessionId(), req.Msg.GetAfterSequence(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, sessionError(err)
	}
	out := make([]*agentv1.AgentSessionInput, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, inputToProto(input))
	}
	return connect.NewResponse(&agentv1.ListSessionInputsResponse{Inputs: out}), nil
}

func (h *SessionHandler) CancelSessionExecution(ctx context.Context, req *connect.Request[agentv1.CancelSessionExecutionRequest]) (*connect.Response[agentv1.CancelSessionExecutionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	active, cancelled, err := h.service.CancelExecution(ctx, owner.UserID, req.Msg.GetSessionId(), req.Msg.GetReason())
	if err != nil {
		return nil, sessionError(err)
	}
	response := &agentv1.CancelSessionExecutionResponse{CancelledQueued: cancelled}
	if active.ID != "" {
		response.Input = inputToProto(active)
	}
	return connect.NewResponse(response), nil
}

func (h *SessionHandler) CloseSession(ctx context.Context, req *connect.Request[agentv1.CloseSessionRequest]) (*connect.Response[agentv1.CloseSessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Close(ctx, owner.UserID, req.Msg.GetSessionId())
	if err != nil {
		return nil, sessionError(err)
	}
	return connect.NewResponse(&agentv1.CloseSessionResponse{Session: sessionToProto(session)}), nil
}

// WatchSessionEvents serves the catch-up page; the live tail is the client's
// cursor poll, matching the task event watch contract.
func (h *SessionHandler) WatchSessionEvents(ctx context.Context, req *connect.Request[agentv1.WatchSessionEventsRequest], stream *connect.ServerStream[agentv1.WatchSessionEventsResponse]) error {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}
	events, err := h.service.Events(ctx, owner.UserID, req.Msg.GetSessionId(), req.Msg.GetAfter(), 500)
	if err != nil {
		return sessionError(err)
	}
	if len(events) > 0 {
		if err := stream.Send(&agentv1.WatchSessionEventsResponse{Events: sessionEventsToProto(events)}); err != nil {
			return err
		}
	}
	return nil
}

func sessionEventsToProto(events []domain.SessionEvent) []*agentv1.AgentSessionEvent {
	out := make([]*agentv1.AgentSessionEvent, 0, len(events))
	for _, event := range events {
		proto := &agentv1.AgentSessionEvent{
			Sequence: event.Sequence, SessionId: event.SessionID, OccurredAt: timestamppb.New(event.OccurredAt),
		}
		var payload map[string]any
		_ = json.Unmarshal(event.Payload, &payload)
		switch event.EventType {
		case "input_accepted":
			proto.Event = &agentv1.AgentSessionEvent_InputAccepted{InputAccepted: &agentv1.SessionInputAccepted{InputId: stringValue(payload, "input_id"), Queued: boolValue(payload, "queued")}}
		case "input_dispatched":
			proto.Event = &agentv1.AgentSessionEvent_InputDispatched{InputDispatched: &agentv1.SessionInputDispatched{InputId: stringValue(payload, "input_id"), TaskId: stringValue(payload, "task_id")}}
		case "input_terminal":
			state := agentv1.AgentSessionInputState(agentv1.AgentSessionInputState_value["AGENT_SESSION_INPUT_STATE_"+uppercase(stringValue(payload, "terminal_state"))])
			proto.Event = &agentv1.AgentSessionEvent_InputTerminal{InputTerminal: &agentv1.SessionInputTerminal{InputId: stringValue(payload, "input_id"), TaskId: stringValue(payload, "task_id"), TerminalState: state, ResultSummary: stringValue(payload, "result_summary")}}
		case "state_changed":
			previous := agentv1.AgentSessionState(agentv1.AgentSessionState_value["AGENT_SESSION_STATE_"+uppercase(stringValue(payload, "previous"))])
			current := agentv1.AgentSessionState(agentv1.AgentSessionState_value["AGENT_SESSION_STATE_"+uppercase(stringValue(payload, "current"))])
			proto.Event = &agentv1.AgentSessionEvent_StateChanged{StateChanged: &agentv1.SessionStateChanged{Previous: previous, Current: current, Reason: stringValue(payload, "reason")}}
		}
		out = append(out, proto)
	}
	return out
}

func stringValue(payload map[string]any, key string) string {
	if value, ok := payload[key].(string); ok {
		return value
	}
	return ""
}

func boolValue(payload map[string]any, key string) bool {
	if value, ok := payload[key].(bool); ok {
		return value
	}
	return false
}

func sessionError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, domain.ErrInvalid), errors.Is(err, domain.ErrSessionInputInvalid):
		code = connect.CodeInvalidArgument
	case errors.Is(err, domain.ErrSessionNotFound), errors.Is(err, domain.ErrNotFound):
		code = connect.CodeNotFound
	case errors.Is(err, domain.ErrSessionClosed):
		code = connect.CodeFailedPrecondition
	case errors.Is(err, domain.ErrSessionInputConflict):
		code = connect.CodeAborted
	case errors.Is(err, domain.ErrSessionBusy):
		code = connect.CodeAborted
	}
	return connect.NewError(code, errors.New("agent session request failed"))
}

func uppercase(value string) string {
	out := []rune(value)
	for i, char := range out {
		if char >= 'a' && char <= 'z' {
			out[i] = char - 'a' + 'A'
		}
	}
	return string(out)
}
