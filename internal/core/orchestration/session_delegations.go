package orchestration

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	workloadv1 "github.com/yangtao121/workos/gen/go/workos/workload/v1"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
)

func delegationResult(d agentdomain.Delegation) map[string]any {
	return map[string]any{"delegationId": d.ID, "title": d.Title, "state": d.State, "worktreeId": d.WorktreeID, "baseCommit": d.BaseCommit, "summary": d.ResultSummary, "artifactId": d.ResultArtifactID}
}

func (s *SessionTools) acquireDelegation(ctx context.Context, scope toolScope, lease, worker, id string, args map[string]any) (map[string]any, error) {
	if s.Delegations == nil || s.Runtime == nil || scope.binding.ReadOnly {
		return nil, agentdomain.ErrProjectDenied
	}
	key, _ := args["requestKey"].(string)
	title, _ := args["title"].(string)
	child, err := s.Delegations.AcquireDelegation(ctx, lease, worker, agentdomain.Delegation{Key: key, Title: title, BindingID: scope.binding.ID, BindingRevision: scope.binding.Revision, SourceID: scope.binding.WorkspaceSourceID}, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	// An already launched grant cannot authorize another native child after
	// an ambiguous response. Review its recorded worktree instead.
	if child.State != "preparing" {
		return nil, agentdomain.ErrSessionInputConflict
	}
	running, stop := s.watchToolScope(ctx, lease, worker, child.ID, "preparing")
	defer stop()
	response, err := s.Runtime.ExecuteWorkspaceOperation(running, connect.NewRequest(delegationOperation(scope, id, child, "delegation.create")))
	if err != nil {
		child.State = "needs_review"
		child.ResultSummary = "Worktree preparation did not confirm a clean committed baseline."
		_, _ = s.Delegations.UpdateTaskDelegation(ctx, lease, worker, child, time.Now().UTC())
		return nil, errors.New("isolated worktree unavailable; a clean committed Git project is required")
	}
	facts := response.Msg.GetResult().AsMap()
	child.WorktreeID, _ = facts["worktreeId"].(string)
	child.BaseCommit, _ = facts["baseCommit"].(string)
	child.State = "running"
	child, err = s.Delegations.UpdateTaskDelegation(ctx, lease, worker, child, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return delegationResult(child), nil
}

func delegationOperation(scope toolScope, id string, child agentdomain.Delegation, name string) *workloadv1.ExecuteWorkspaceOperationRequest {
	return &workloadv1.ExecuteWorkspaceOperationRequest{OperationId: id, Operation: name, DelegationId: child.ID, ParentTaskId: scope.task.TaskID, OwnerUserId: scope.task.OwnerUserID, ProjectId: scope.task.ProjectID, WorkspaceBindingId: scope.binding.ID, WorkspaceRevision: scope.binding.Revision, WorkspaceSourceId: scope.binding.WorkspaceSourceID, ReadOnly: scope.binding.ReadOnly}
}

func (s *SessionTools) finishDelegation(ctx context.Context, scope toolScope, lease, worker, id string, child agentdomain.Delegation, args map[string]any) (map[string]any, error) {
	state, _ := args["state"].(string)
	summary, _ := args["summary"].(string)
	if state != "completed" && state != "failed" && state != "cancelled" {
		return nil, agentdomain.ErrInvalid
	}
	if len(summary) > 2048 {
		summary = summary[:2048]
		for !utf8.ValidString(summary) {
			summary = summary[:len(summary)-1]
		}
	}
	if child.Terminal() {
		if child.State != state || child.ResultSummary != summary {
			return nil, agentdomain.ErrSessionInputConflict
		}
		return delegationResult(child), nil
	}
	if child.State != "running" || s.Runtime == nil || s.Publications == nil {
		return nil, agentdomain.ErrProjectDenied
	}
	running, stop := s.watchToolScope(ctx, lease, worker, child.ID, "running")
	defer stop()
	result, err := s.Runtime.ExecuteWorkspaceOperation(running, connect.NewRequest(delegationOperation(scope, id, child, "delegation.diff")))
	if err != nil {
		return nil, err
	}
	patch, _ := result.Msg.GetResult().AsMap()["diff"].(string)
	if patch != "" {
		artifact, _, err := s.Publications.MaterializeDelegationArtifact(ctx, lease, worker, child.ID, "Delegation result: "+child.Title, []byte(patch))
		if err != nil {
			return nil, err
		}
		child.ResultArtifactID = artifact.GetId()
	}
	child.State, child.ResultSummary = state, summary
	child, err = s.Delegations.UpdateTaskDelegation(ctx, lease, worker, child, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return delegationResult(child), nil
}
