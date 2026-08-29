package transport

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	"github.com/yangtao121/workos/internal/core/agent/application"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// maxPolicyRequestBytes bounds every decoded policy request before business
// code runs: legal bodies are far smaller, so the cap rejects oversize and
// gzip-bomb payloads as ResourceExhausted without executing anything.
const maxPolicyRequestBytes = 16 * 1024

// PolicyHandler is the public, identity-protected AgentAppPolicyService. The
// App bridge/iframe surface has no route to it; only the owner decides how
// their installed apps may run.
type PolicyHandler struct {
	service *application.PolicyService
}

// NewPolicyConnectHandler returns the Connect path and handler for the public
// policy service.
func NewPolicyConnectHandler(service *application.PolicyService) (string, http.Handler) {
	return agentv1connect.NewAgentAppPolicyServiceHandler(&PolicyHandler{service: service},
		connect.WithReadMaxBytes(maxPolicyRequestBytes))
}

func policySpecToProto(spec domain.PolicySpec) *agentv1.AppAgentPolicySpec {
	return &agentv1.AppAgentPolicySpec{
		ExecutionMode:                    policyModeToProto(spec.Mode),
		MaxOutputTokensPerTask:           spec.MaxOutputTokensPerTask,
		MaxRuntimeSecondsPerTask:         spec.MaxRuntimeSecondsPerTask,
		MaxTasksPerUtcDay:                spec.MaxTasksPerUTCDay,
		MaxReservedOutputTokensPerUtcDay: spec.MaxReservedOutputTokensPerUTCDay,
	}
}

func policyModeToProto(mode domain.PolicyMode) agentv1.AppAgentExecutionMode {
	switch mode {
	case domain.PolicyModeAllow:
		return agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_ALLOW
	case domain.PolicyModeRequireApproval:
		return agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_REQUIRE_APPROVAL
	case domain.PolicyModeBlock:
		return agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_BLOCK
	default:
		return agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_UNSPECIFIED
	}
}

func policyToProto(policy domain.Policy) *agentv1.AppAgentPolicy {
	source := agentv1.AppAgentPolicySource_APP_AGENT_POLICY_SOURCE_EXPLICIT
	if policy.Source == domain.PolicySourceSystemDefault {
		source = agentv1.AppAgentPolicySource_APP_AGENT_POLICY_SOURCE_SYSTEM_DEFAULT
	}
	return &agentv1.AppAgentPolicy{
		ProjectId:      policy.ProjectID,
		InstallationId: policy.AppInstanceID,
		Spec:           policySpecToProto(policy.Spec),
		Source:         source,
		PolicyRevision: policy.Revision,
	}
}

// policySpecFromProto maps an unknown enum value to an empty mode so the
// domain validator rejects it as InvalidArgument at the boundary.
func policySpecFromProto(spec *agentv1.AppAgentPolicySpec) domain.PolicySpec {
	if spec == nil {
		return domain.PolicySpec{}
	}
	mode := domain.PolicyMode("")
	switch spec.GetExecutionMode() {
	case agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_ALLOW:
		mode = domain.PolicyModeAllow
	case agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_REQUIRE_APPROVAL:
		mode = domain.PolicyModeRequireApproval
	case agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_BLOCK:
		mode = domain.PolicyModeBlock
	}
	return domain.PolicySpec{
		Mode:                             mode,
		MaxOutputTokensPerTask:           spec.GetMaxOutputTokensPerTask(),
		MaxRuntimeSecondsPerTask:         spec.GetMaxRuntimeSecondsPerTask(),
		MaxTasksPerUTCDay:                spec.GetMaxTasksPerUtcDay(),
		MaxReservedOutputTokensPerUTCDay: spec.GetMaxReservedOutputTokensPerUtcDay(),
	}
}

func (h *PolicyHandler) GetAppPolicy(ctx context.Context, req *connect.Request[agentv1.GetAppPolicyRequest]) (*connect.Response[agentv1.GetAppPolicyResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	policy, err := h.service.EffectivePolicy(ctx, id.UserID, req.Msg.GetProjectId(), req.Msg.GetInstallationId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&agentv1.GetAppPolicyResponse{Policy: policyToProto(policy)}), nil
}

func (h *PolicyHandler) SetAppPolicy(ctx context.Context, req *connect.Request[agentv1.SetAppPolicyRequest]) (*connect.Response[agentv1.SetAppPolicyResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	policy, _, err := h.service.SetPolicy(ctx, application.SetPolicyInput{
		OwnerUserID:            id.UserID,
		ProjectID:              req.Msg.GetProjectId(),
		AppInstanceID:          req.Msg.GetInstallationId(),
		Spec:                   policySpecFromProto(req.Msg.GetSpec()),
		ExpectedPolicyRevision: req.Msg.GetExpectedPolicyRevision(),
		IdempotencyKey:         req.Msg.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&agentv1.SetAppPolicyResponse{Policy: policyToProto(policy)}), nil
}
