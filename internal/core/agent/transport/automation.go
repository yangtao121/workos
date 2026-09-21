package transport

import (
	"connectrpc.com/connect"
	"context"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/core/agent/application"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
	"google.golang.org/protobuf/encoding/protojson"
)

func directiveToProto(value *domain.SessionDirective) *agentv1.SessionDirective {
	if value == nil {
		return nil
	}
	kind := agentv1.SessionDirectiveKind_SESSION_DIRECTIVE_KIND_UNSPECIFIED
	switch value.Kind {
	case "create_goal":
		kind = agentv1.SessionDirectiveKind_SESSION_DIRECTIVE_KIND_CREATE_GOAL
	case "resume_goal":
		kind = agentv1.SessionDirectiveKind_SESSION_DIRECTIVE_KIND_RESUME_GOAL
	case "pause_goal":
		kind = agentv1.SessionDirectiveKind_SESSION_DIRECTIVE_KIND_PAUSE_GOAL
	}
	return &agentv1.SessionDirective{Kind: kind, GoalRef: value.GoalRef, ExpectedRevision: value.ExpectedRevision, Objective: value.Objective, MaxRounds: value.MaxRounds}
}

func directiveFromProto(value *agentv1.SessionDirective) domain.SessionDirective {
	kind := ""
	switch value.GetKind() {
	case agentv1.SessionDirectiveKind_SESSION_DIRECTIVE_KIND_CREATE_GOAL:
		kind = "create_goal"
	case agentv1.SessionDirectiveKind_SESSION_DIRECTIVE_KIND_RESUME_GOAL:
		kind = "resume_goal"
	case agentv1.SessionDirectiveKind_SESSION_DIRECTIVE_KIND_PAUSE_GOAL:
		kind = "pause_goal"
	}
	return domain.SessionDirective{Kind: kind, GoalRef: value.GetGoalRef(), ExpectedRevision: value.GetExpectedRevision(), Objective: value.GetObjective(), MaxRounds: value.GetMaxRounds()}
}

func (d *sessionTaskDispatcher) DispatchDirective(ctx context.Context, owner, project, provider, key, session string, directive domain.SessionDirective) (domain.Task, error) {
	if err := directive.Validate(); err != nil {
		return domain.Task{}, err
	}
	goal := "Apply session goal control"
	if directive.Kind == "create_goal" {
		goal = directive.Objective
	}
	input := &agentv1.AgentTaskInput{TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project}}, Goal: goal, AgentSessionId: session, SessionDirective: directiveToProto(&directive)}
	payload, err := protojson.Marshal(input)
	if err != nil {
		return domain.Task{}, domain.ErrInvalid
	}
	return d.submitter.Submit(ctx, application.SubmitInput{OwnerUserID: owner, ProjectID: project, ProviderID: provider, IdempotencyKey: key, Payload: payload})
}

func (h *SessionHandler) RequestSessionGoalPause(ctx context.Context, req *connect.Request[agentv1.RequestSessionGoalPauseRequest]) (*connect.Response[agentv1.RequestSessionGoalPauseResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.RequestGoalPause(ctx, owner.UserID, req.Msg.GetSessionId(), req.Msg.GetIdempotencyKey(), req.Msg.GetGoalRef())
	if err != nil {
		return nil, sessionError(err)
	}
	return connect.NewResponse(&agentv1.RequestSessionGoalPauseResponse{Session: sessionToProto(session)}), nil
}
