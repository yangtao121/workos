package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

type AgentTasks interface {
	Submit(context.Context, agentapp.SubmitInput) (agentdomain.Task, error)
	GetByIdempotency(context.Context, string, string) (agentdomain.Task, error)
	// App bridge surface: durable (owner, app instance, client key)
	// adjudication and provenance-bound reads live in the Agent module.
	SubmitForApp(context.Context, agentapp.AppSubmitInput) (agentdomain.Task, error)
	GetAppTaskByIdempotency(context.Context, string, string, string) (agentdomain.Task, string, bool, error)
	GetAppTask(context.Context, string, string, string) (agentdomain.Task, string, error)
	AppTaskEvents(context.Context, string, string, string, int64, int) ([]agentdomain.Event, error)
}

type Projects interface {
	Get(context.Context, string, string) (projectdomain.Project, error)
}

type TaskRouter struct {
	agents          AgentTasks
	projects        Projects
	defaultProvider string
}

func NewTaskRouter(agents AgentTasks, projects Projects, defaultProvider string) (*TaskRouter, error) {
	defaultProvider = strings.TrimSpace(defaultProvider)
	if agents == nil || projects == nil || defaultProvider == "" {
		return nil, errors.New("task router requires agent, project, and default provider dependencies")
	}
	return &TaskRouter{agents: agents, projects: projects, defaultProvider: defaultProvider}, nil
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

// SubmitForApp routes one App-principal project task through the same
// provider snapshot semantics as user submissions: replay first (the mapping
// digest decides replay versus conflict, so the first provider snapshot never
// drifts with later binding changes), then the project binding — or global
// default — is snapshotted at creation time. The project scope itself is the
// installation's fact and is not overridable here.
func (r *TaskRouter) SubmitForApp(ctx context.Context, input agentapp.AppSubmitInput) (agentdomain.Task, error) {
	task, digest, found, err := r.agents.GetAppTaskByIdempotency(ctx, input.OwnerUserID, input.AppInstanceID, input.ClientIdempotencyKey)
	if err != nil {
		return agentdomain.Task{}, err
	}
	if found {
		if digest != input.RequestDigest {
			return agentdomain.Task{}, agentdomain.ErrIdempotencyConflict
		}
		return task, nil
	}

	providerID := r.defaultProvider
	project, getErr := r.projects.Get(ctx, input.OwnerUserID, input.ProjectID)
	switch {
	case errors.Is(getErr, projectdomain.ErrNotFound), errors.Is(getErr, projectdomain.ErrInvalid):
		return agentdomain.Task{}, agentdomain.ErrProjectDenied
	case getErr != nil:
		return agentdomain.Task{}, fmt.Errorf("resolve project for app task routing: %w", getErr)
	case project.ArchivedAt != nil:
		return agentdomain.Task{}, agentdomain.ErrProjectDenied
	case project.HarnessBinding != nil && strings.TrimSpace(project.HarnessBinding.ProviderID) != "":
		providerID = strings.TrimSpace(project.HarnessBinding.ProviderID)
	}

	input.ProviderID = providerID
	return r.agents.SubmitForApp(ctx, input)
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
