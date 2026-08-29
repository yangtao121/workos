package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

type AgentTasks interface {
	Submit(context.Context, agentapp.SubmitInput) (agentdomain.Task, error)
	GetByIdempotency(context.Context, string, string) (agentdomain.Task, error)
	// App bridge surface: durable (owner, app instance, client key)
	// adjudication and provenance-bound reads live in the Agent module.
	SubmitForApp(context.Context, agentapp.AppSubmitInput) (agentdomain.Task, error)
	SubmitForAppApproval(context.Context, agentapp.AppSubmitInput) (agentdomain.Task, agentdomain.Approval, error)
	GetAppTaskByIdempotency(context.Context, string, string, string) (agentdomain.Task, string, bool, error)
	GetAppTask(context.Context, string, string, string) (agentdomain.Task, string, error)
	AppTaskEvents(context.Context, string, string, string, int64, int) ([]agentdomain.Event, error)
}

// AgentAppPolicies resolves the effective policy for one App installation.
type AgentAppPolicies interface {
	EffectivePolicy(ctx context.Context, ownerUserID, projectID, appInstanceID string) (agentdomain.Policy, error)
}

// AgentProviderCapabilities resolves the budget-contract capabilities of one
// provider.
type AgentProviderCapabilities interface {
	Capabilities(ctx context.Context, providerID string) (agentports.ProviderCapabilities, error)
}

type Projects interface {
	Get(context.Context, string, string) (projectdomain.Project, error)
}

type TaskRouter struct {
	agents          AgentTasks
	projects        Projects
	policies        AgentAppPolicies
	providers       AgentProviderCapabilities
	defaultProvider string
}

func NewTaskRouter(agents AgentTasks, projects Projects, policies AgentAppPolicies, providers AgentProviderCapabilities, defaultProvider string) (*TaskRouter, error) {
	defaultProvider = strings.TrimSpace(defaultProvider)
	if agents == nil || projects == nil || policies == nil || providers == nil || defaultProvider == "" {
		return nil, errors.New("task router requires agent, project, policy, provider, and default provider dependencies")
	}
	return &TaskRouter{agents: agents, projects: projects, policies: policies, providers: providers, defaultProvider: defaultProvider}, nil
}

func (r *TaskRouter) Submit(ctx context.Context, input agentapp.SubmitInput) (agentdomain.Task, error) {
	existing, err := r.agents.GetByIdempotency(ctx, input.OwnerUserID, input.IdempotencyKey)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, agentdomain.ErrNotFound) {
		return agentdomain.Task{}, err
	}

	providerID := r.defaultProvider
	if input.ProjectID != "" {
		project, getErr := r.projects.Get(ctx, input.OwnerUserID, input.ProjectID)
		switch {
		case errors.Is(getErr, projectdomain.ErrNotFound), errors.Is(getErr, projectdomain.ErrInvalid):
			return agentdomain.Task{}, agentdomain.ErrProjectDenied
		case getErr != nil:
			return agentdomain.Task{}, fmt.Errorf("resolve project for task routing: %w", getErr)
		case project.ArchivedAt != nil:
			return agentdomain.Task{}, agentdomain.ErrProjectDenied
		case project.HarnessBinding != nil && strings.TrimSpace(project.HarnessBinding.ProviderID) != "":
			providerID = strings.TrimSpace(project.HarnessBinding.ProviderID)
		}
	}

	input.ProviderID = providerID
	return r.agents.Submit(ctx, input)
}

// SubmitForApp routes one App-principal project task through the fixed
// adjudication chain of ADR-0005: replay first (the mapping digest decides
// replay versus conflict, so the first provider snapshot, policy, and budget
// never drift with later changes), then the project binding — or global
// default — is snapshotted, the effective policy is resolved, the provider's
// budget contract is verified, and finally the execution mode routes between
// the atomic allow enqueue and the atomic require-approval handoff. block,
// missing provider capabilities, and exhausted quota fail closed without
// consuming the App run key. The project scope itself is the installation's
// fact and is not overridable here.
func (r *TaskRouter) SubmitForApp(ctx context.Context, input agentapp.AppSubmitInput) (agentdomain.Task, error) {
	mode, replayed, err := r.adjudicateAppRun(ctx, &input)
	if err != nil {
		return agentdomain.Task{}, err
	}
	if replayed != nil {
		return *replayed, nil
	}
	if mode == agentdomain.PolicyModeRequireApproval {
		task, _, err := r.agents.SubmitForAppApproval(ctx, input)
		return task, err
	}
	return r.agents.SubmitForApp(ctx, input)
}

// adjudicateAppRun runs the shared pre-enqueue chain for a fresh App run and
// fills the server-derived enforcement into the input. A non-nil task is a
// replay answer that already short-circuited the chain; otherwise the
// resolved execution mode selects the enqueue path.
func (r *TaskRouter) adjudicateAppRun(ctx context.Context, input *agentapp.AppSubmitInput) (agentdomain.PolicyMode, *agentdomain.Task, error) {
	task, digest, found, err := r.agents.GetAppTaskByIdempotency(ctx, input.OwnerUserID, input.AppInstanceID, input.ClientIdempotencyKey)
	if err != nil {
		return "", nil, err
	}
	if found {
		if digest != input.RequestDigest {
			return "", nil, agentdomain.ErrIdempotencyConflict
		}
		return "", &task, nil
	}

	providerID := r.defaultProvider
	project, getErr := r.projects.Get(ctx, input.OwnerUserID, input.ProjectID)
	switch {
	case errors.Is(getErr, projectdomain.ErrNotFound), errors.Is(getErr, projectdomain.ErrInvalid):
		return "", nil, agentdomain.ErrProjectDenied
	case getErr != nil:
		return "", nil, fmt.Errorf("resolve project for app task routing: %w", getErr)
	case project.ArchivedAt != nil:
		return "", nil, agentdomain.ErrProjectDenied
	case project.HarnessBinding != nil && strings.TrimSpace(project.HarnessBinding.ProviderID) != "":
		providerID = strings.TrimSpace(project.HarnessBinding.ProviderID)
	}

	// Effective policy: never wider than the grant (already authorized by the
	// App Agent service) and never unlimited.
	policy, err := r.policies.EffectivePolicy(ctx, input.OwnerUserID, input.ProjectID, input.AppInstanceID)
	if err != nil {
		if errors.Is(err, agentdomain.ErrNotFound) {
			return "", nil, agentdomain.ErrProjectDenied
		}
		return "", nil, fmt.Errorf("resolve app policy: %w", err)
	}
	if policy.Spec.Mode == agentdomain.PolicyModeBlock {
		return "", nil, agentdomain.ErrPolicyBlocksRuns
	}
	capabilities, err := r.providers.Capabilities(ctx, providerID)
	if err != nil {
		if errors.Is(err, agentdomain.ErrNotFound) {
			return "", nil, agentdomain.ErrProviderCapabilityMissing
		}
		return "", nil, fmt.Errorf("resolve provider capabilities: %w", err)
	}
	if !capabilities.Complete() {
		return "", nil, agentdomain.ErrProviderCapabilityMissing
	}
	input.ProviderID = providerID
	input.AppID = policy.AppID
	input.Enforcement = agentapp.AppRunEnforcement{
		Policy: agentports.PolicySnapshot{
			Source:     policy.Source,
			Revision:   policy.Revision,
			SpecDigest: policy.Spec.Digest(),
			Spec:       policy.Spec,
		},
		MaxOutputTokensTask:   policy.Spec.MaxOutputTokensPerTask,
		MaxRuntimeSecondsTask: policy.Spec.MaxRuntimeSecondsPerTask,
		Daily: agentports.DailyAllowance{
			MaxTasks:                policy.Spec.MaxTasksPerUTCDay,
			MaxReservedOutputTokens: policy.Spec.MaxReservedOutputTokensPerUTCDay,
		},
	}
	return policy.Spec.Mode, nil, nil
}

// GetAppTask exposes the Agent module's provenance-bound task read.
func (r *TaskRouter) GetAppTask(ctx context.Context, ownerID, appInstanceID, taskID string) (agentdomain.Task, string, error) {
	return r.agents.GetAppTask(ctx, ownerID, appInstanceID, taskID)
}

// GetAppTaskByIdempotency exposes the Agent module's replay projection read:
// the stored task plus the consumed request digest, or found=false.
func (r *TaskRouter) GetAppTaskByIdempotency(ctx context.Context, ownerID, appInstanceID, clientKey string) (agentdomain.Task, string, bool, error) {
	return r.agents.GetAppTaskByIdempotency(ctx, ownerID, appInstanceID, clientKey)
}

// AppTaskEvents exposes the Agent module's provenance-bound event read.
func (r *TaskRouter) AppTaskEvents(ctx context.Context, ownerID, appInstanceID, taskID string, after int64, limit int) ([]agentdomain.Event, error) {
	return r.agents.AppTaskEvents(ctx, ownerID, appInstanceID, taskID, after, limit)
}
