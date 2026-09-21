package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	workloadv1 "github.com/yangtao121/workos/gen/go/workos/workload/v1"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"google.golang.org/protobuf/types/known/structpb"
)

// SessionTools derives scope at every operation and rechecks it while a
// command is running. The model never supplies identity headers or host paths.
type SessionTools struct {
	Delegations   agentports.DelegationRepository
	Installations *projectapp.InstallationService
	Interactions  *agentapp.InteractionService
	Publications  *TaskArtifactMaterializer
	Pool          *pgxpool.Pool
	Tasks         agentports.TaskStreamStore
	Sessions      agentports.SessionRepository
	Projects      *projectapp.Service
	Workspaces    *projectapp.WorkspaceService
	Artifacts     *artifactapp.Service
	Runtime       workloadv1connect.WorkspaceExecutionServiceClient
}
type toolScope struct {
	task    agentports.TaskStreamFacts
	session agentdomain.Session
	binding projectdomain.WorkspaceBinding
}

func (s *SessionTools) scope(ctx context.Context, lease, worker string) (toolScope, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return toolScope{}, err
	}
	defer tx.Rollback(ctx)
	facts, err := s.Tasks.LockTaskArtifactStream(ctx, tx, lease, worker, time.Now().UTC())
	if err != nil {
		return toolScope{}, err
	}
	if facts.CancellationRequested {
		return toolScope{}, agentdomain.ErrLeaseLost
	}
	var input struct {
		SessionID string `json:"agentSessionId"`
	}
	if err := json.Unmarshal(facts.Input, &input); err != nil || input.SessionID == "" {
		return toolScope{}, agentdomain.ErrInvalid
	}
	// Release the lease lock before other module calls and remote execution.
	if err := tx.Commit(ctx); err != nil {
		return toolScope{}, err
	}
	session, err := s.Sessions.GetSession(ctx, facts.OwnerUserID, input.SessionID)
	if err != nil {
		return toolScope{}, err
	}
	if session.ProjectID != facts.ProjectID || session.ActiveTaskID != facts.TaskID || session.WorkspaceBindingID == "" || session.ProviderID != facts.ProviderID {
		return toolScope{}, agentdomain.ErrProjectDenied
	}
	project, err := s.Projects.Get(ctx, facts.OwnerUserID, facts.ProjectID)
	if err != nil {
		return toolScope{}, err
	}
	if project.ArchivedAt != nil {
		return toolScope{}, agentdomain.ErrProjectDenied
	}
	binding, err := s.Workspaces.Get(ctx, facts.OwnerUserID, session.WorkspaceBindingID)
	if err != nil {
		return toolScope{}, err
	}
	if binding.ProjectID != facts.ProjectID || binding.Revision != session.WorkspaceBindingRevision || binding.State != projectdomain.WorkspaceBindingActive {
		return toolScope{}, agentdomain.ErrProjectDenied
	}
	return toolScope{facts, session, binding}, nil
}
func (s *SessionTools) Execute(ctx context.Context, lease, worker, id, operation string, args map[string]any) (map[string]any, error) {
	return s.execute(ctx, lease, worker, id, operation, "", args)
}

func (s *SessionTools) ExecuteDelegated(ctx context.Context, lease, worker, id, operation, delegation string, args map[string]any) (map[string]any, error) {
	return s.execute(ctx, lease, worker, id, operation, delegation, args)
}

func (s *SessionTools) execute(ctx context.Context, lease, worker, id, operation, delegation string, args map[string]any) (map[string]any, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.Version() != 7 {
		return nil, agentdomain.ErrInvalid
	}
	scope, err := s.scope(ctx, lease, worker)
	if err != nil {
		return nil, err
	}
	var child agentdomain.Delegation
	if delegation != "" {
		if s.Delegations == nil {
			return nil, agentdomain.ErrProjectDenied
		}
		child, err = s.Delegations.GetTaskDelegation(ctx, lease, worker, delegation, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if child.BindingID != scope.binding.ID || child.BindingRevision != scope.binding.Revision || child.SourceID != scope.binding.WorkspaceSourceID {
			return nil, agentdomain.ErrProjectDenied
		}
		if operation == "delegation.finish" {
			return s.finishDelegation(ctx, scope, lease, worker, id, child, args)
		}
		if child.State != "running" {
			return nil, agentdomain.ErrProjectDenied
		}
		if operation == "delegation.acquire" || operation == "session.control" || operation == "interaction.ask" || operation == "artifact.create" || strings.HasPrefix(operation, "preview.") {
			return nil, agentdomain.ErrProjectDenied
		}
	}
	switch operation {
	case "delegation.acquire":
		return s.acquireDelegation(ctx, scope, lease, worker, id, args)
	case "session.control":
		return map[string]any{"pauseGoalRef": scope.session.GoalPauseRef}, nil
	case "interaction.ask":
		if s.Interactions == nil {
			return nil, agentdomain.ErrInvalid
		}
		payload, err := json.Marshal(args["questions"])
		if err != nil {
			return nil, err
		}
		var questions []agentdomain.ExecutionQuestion
		if err := json.Unmarshal(payload, &questions); err != nil {
			return nil, agentdomain.ErrInvalid
		}
		key, _ := args["requestKey"].(string)
		interaction, err := s.Interactions.Ask(ctx, scope.task.OwnerUserID, scope.task.ProjectID, scope.task.TaskID, lease, worker, key, questions)
		if err != nil {
			return nil, err
		}
		answers := make([]any, 0, len(interaction.Answers))
		for _, a := range interaction.Answers {
			selected := make([]any, len(a.Selected))
			for i, label := range a.Selected {
				selected[i] = label
			}
			answers = append(answers, map[string]any{"questionId": a.QuestionID, "selected": selected, "text": a.Text})
		}
		return map[string]any{"state": interaction.State, "answers": answers}, nil
	case "app.list":
		if s.Installations == nil {
			return nil, errors.New("application tools unavailable")
		}
		page, err := s.Installations.ListInstalled(ctx, scope.task.OwnerUserID, scope.task.ProjectID, "", 50)
		if err != nil {
			return nil, err
		}
		items := make([]any, 0, len(page.Items))
		for _, app := range page.Items {
			items = append(items, map[string]any{"installationId": app.ID, "appId": app.AppID, "version": app.Version})
		}
		return map[string]any{"applications": items}, nil
	case "workspace.info":
		return map[string]any{"workspaceId": scope.binding.ID, "revision": scope.binding.Revision, "readOnly": scope.binding.ReadOnly, "workingDirectory": "/workspace"}, nil
	case "project.info":
		project, err := s.Projects.Get(ctx, scope.task.OwnerUserID, scope.task.ProjectID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"projectId": project.ID, "name": project.Name}, nil
	case "artifact.list":
		page, err := s.Artifacts.List(ctx, scope.task.OwnerUserID, scope.task.ProjectID, "", 50)
		if err != nil {
			return nil, err
		}
		artifacts := make([]any, 0, len(page.Items))
		for _, artifact := range page.Items {
			artifacts = append(artifacts, map[string]any{"id": artifact.ID, "type": artifact.Type, "title": artifact.Title})
		}
		return map[string]any{"artifacts": artifacts}, nil
	case "artifact.create":
		if s.Publications == nil {
			return nil, errors.New("artifact tools unavailable")
		}
		key, _ := args["outputKey"].(string)
		title, _ := args["title"].(string)
		typ, _ := args["type"].(string)
		content, _ := args["content"].(string)
		artifact, _, err := s.Publications.MaterializeTaskArtifact(ctx, lease, worker, key, title, typ, []byte(content))
		if err != nil {
			return nil, err
		}
		return map[string]any{"artifactId": artifact.GetId(), "title": artifact.GetTitle(), "digest": artifact.GetDigest()}, nil

	case "artifact.read":
		artifactID, _ := args["artifactId"].(string)
		fact, content, err := s.Artifacts.GetReview(ctx, scope.task.OwnerUserID, artifactID)
		if err != nil {
			return nil, err
		}
		if fact.ProjectID != scope.task.ProjectID {
			return nil, agentdomain.ErrProjectDenied
		}
		return map[string]any{"artifactId": fact.ID, "title": fact.Title, "content": string(content.Content)}, nil
	}
	if !strings.HasPrefix(operation, "fs.") && operation != "shell.run" && operation != "preview.start" && operation != "preview.list" && operation != "preview.stop" {
		return nil, agentdomain.ErrInvalid
	}
	if s.Runtime == nil {
		return nil, errors.New("workspace execution unavailable")
	}
	// Cancellation, revocation, binding revision changes, and lease loss close
	// the Runtime request, which owns process-group/container teardown.
	running, stop := s.watchToolScope(ctx, lease, worker, delegation, "running")
	defer stop()
	arguments, err := structpb.NewStruct(args)
	if err != nil {
		return nil, agentdomain.ErrInvalid
	}
	result, err := s.Runtime.ExecuteWorkspaceOperation(running, connect.NewRequest(&workloadv1.ExecuteWorkspaceOperationRequest{DelegationId: delegation, ParentTaskId: map[bool]string{true: scope.task.TaskID}[delegation != ""], WorkspaceBindingId: scope.binding.ID, WorkspaceRevision: scope.binding.Revision, OwnerUserId: scope.task.OwnerUserID, ProjectId: scope.task.ProjectID, WorkspaceSourceId: scope.binding.WorkspaceSourceID, ReadOnly: scope.binding.ReadOnly, OperationId: id, Operation: operation, Arguments: arguments}))
	if err != nil {
		return nil, err
	}
	return result.Msg.GetResult().AsMap(), nil
}

func (s *SessionTools) ValidateInteraction(ctx context.Context, lease, worker string) error {
	_, err := s.scope(ctx, lease, worker)
	return err
}

// watchToolScope bounds the authority of long Runtime calls, including worktree
// preparation and diff generation, to the current task lease and project grant.
func (s *SessionTools) watchToolScope(ctx context.Context, lease, worker, delegation, state string) (context.Context, context.CancelFunc) {
	running, cancel := context.WithCancel(ctx)
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-running.Done():
				return
			case <-ticker.C:
				if delegation != "" {
					child, err := s.Delegations.GetTaskDelegation(running, lease, worker, delegation, time.Now().UTC())
					if err != nil || child.State != state {
						cancel()
						return
					}
				}
				if _, err := s.scope(running, lease, worker); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return running, func() { cancel(); <-settled }
}
