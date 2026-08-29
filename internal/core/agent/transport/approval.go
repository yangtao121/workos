package transport

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/internal/core/agent/application"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// maxApprovalRequestBytes bounds every decoded approval request before
// business code runs.
const maxApprovalRequestBytes = 16 * 1024

// ApprovalHandler is the public, identity-protected AgentApprovalService.
// Approvals are owner-only facts: the App bridge can only observe the
// waiting task event, never list or decide.
type ApprovalHandler struct {
	service *application.ApprovalService
}

// NewApprovalConnectHandler returns the Connect path and handler for the
// public approval service.
func NewApprovalConnectHandler(service *application.ApprovalService) (string, http.Handler) {
	return agentv1connect.NewAgentApprovalServiceHandler(&ApprovalHandler{service: service},
		connect.WithReadMaxBytes(maxApprovalRequestBytes))
}

func approvalStateToProto(state domain.ApprovalState) agentv1.AppAgentApprovalState {
	switch state {
	case domain.ApprovalPending:
		return agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_PENDING
	case domain.ApprovalApproved:
		return agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_APPROVED
	case domain.ApprovalRejected:
		return agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_REJECTED
	case domain.ApprovalExpired:
		return agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_EXPIRED
	default:
		return agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_UNSPECIFIED
	}
}

// approvalStateFromProto maps the wire state filter. UNSPECIFIED means no
// filter; any unknown value is a client contract violation, never an
// all-states wildcard.
func approvalStateFromProto(state agentv1.AppAgentApprovalState) (domain.ApprovalState, error) {
	switch state {
	case agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_UNSPECIFIED:
		return "", nil
	case agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_PENDING:
		return domain.ApprovalPending, nil
	case agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_APPROVED:
		return domain.ApprovalApproved, nil
	case agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_REJECTED:
		return domain.ApprovalRejected, nil
	case agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_EXPIRED:
		return domain.ApprovalExpired, nil
	default:
		return "", fmt.Errorf("unknown approval state filter: %w", domain.ErrInvalid)
	}
}

func approvalToProto(approval domain.Approval) *agentv1.AgentApproval {
	result := &agentv1.AgentApproval{
		ApprovalId:               approval.ID,
		TaskId:                   approval.TaskID,
		ProjectId:                approval.ProjectID,
		InstallationId:           approval.AppInstanceID,
		AppId:                    approval.AppID,
		GoalExcerpt:              approval.GoalExcerpt,
		ProviderId:               approval.ProviderID,
		MaxOutputTokensPerTask:   approval.Spec.MaxOutputTokensPerTask,
		MaxRuntimeSecondsPerTask: approval.Spec.MaxRuntimeSecondsPerTask,
		PolicyRevision:           approval.Revision,
		State:                    approvalStateToProto(approval.State),
		CreatedAt:                timestamppb.New(approval.CreatedAt),
	}
	if !approval.DecidedAt.IsZero() {
		decided := approval.DecidedAt
		result.DecidedAt = timestamppb.New(decided)
	}
	return result
}

func (h *ApprovalHandler) ListApprovals(ctx context.Context, req *connect.Request[agentv1.ListApprovalsRequest]) (*connect.Response[agentv1.ListApprovalsResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	limit, cursor := 0, ""
	if req.Msg.Page != nil {
		limit, cursor = int(req.Msg.Page.PageSize), req.Msg.Page.PageToken
	}
	state, err := approvalStateFromProto(req.Msg.GetState())
	if err != nil {
		return nil, mapError(err)
	}
	approvals, next, err := h.service.List(ctx, id.UserID, req.Msg.GetProjectId(), state, cursor, limit)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*agentv1.AgentApproval, 0, len(approvals))
	for _, approval := range approvals {
		items = append(items, approvalToProto(approval))
	}
	// The service already resolved the next-page token from a limit+1 probe:
	// present only when a further page exists, never on a full final page.
	return connect.NewResponse(&agentv1.ListApprovalsResponse{Approvals: items, Page: &commonv1.PageResponse{NextPageToken: next}}), nil
}

func (h *ApprovalHandler) GetApproval(ctx context.Context, req *connect.Request[agentv1.GetApprovalRequest]) (*connect.Response[agentv1.GetApprovalResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	approval, err := h.service.Get(ctx, id.UserID, req.Msg.GetApprovalId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&agentv1.GetApprovalResponse{Approval: approvalToProto(approval)}), nil
}

func (h *ApprovalHandler) DecideApproval(ctx context.Context, req *connect.Request[agentv1.DecideApprovalRequest]) (*connect.Response[agentv1.DecideApprovalResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	decision := domain.ApprovalDecision("")
	switch req.Msg.GetDecision() {
	case agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_APPROVE:
		decision = domain.ApprovalDecisionApprove
	case agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_REJECT:
		decision = domain.ApprovalDecisionReject
	}
	approval, err := h.service.Decide(ctx, application.DecideInput{
		OwnerUserID: id.UserID, ApprovalID: req.Msg.GetApprovalId(),
		Decision: decision, IdempotencyKey: req.Msg.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&agentv1.DecideApprovalResponse{Approval: approvalToProto(approval)}), nil
}
