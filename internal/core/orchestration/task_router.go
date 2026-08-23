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
