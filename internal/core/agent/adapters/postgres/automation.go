package postgres

import (
	"context"
	"encoding/json"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/core/agent/adapters/postgres/agentdb"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"google.golang.org/protobuf/encoding/protojson"
)

func (r *SessionRepository) GetGoalPauseRequest(ctx context.Context, session, key string) (string, error) {
	ref, err := r.queries.GetGoalPauseRequest(ctx, agentdb.GetGoalPauseRequestParams{SessionID: session, IdempotencyKey: key})
	return ref, sessionError("get goal pause request", err)
}

func (r *SessionRepository) RecordGoalPauseRequest(ctx context.Context, owner, session, key, ref string, now time.Time) error {
	if err := r.queries.InsertGoalPauseRequest(ctx, agentdb.InsertGoalPauseRequestParams{SessionID: session, IdempotencyKey: key, GoalRef: ref, CreatedAt: timestamp(now)}); err != nil {
		return err
	}
	return r.queries.RequestSessionGoalPause(ctx, agentdb.RequestSessionGoalPauseParams{OwnerUserID: owner, SessionID: session, GoalPauseRef: ref, UpdatedAt: timestamp(now)})
}

// The projection and its canonical event commit in the same task transaction.
// Native goal ids remain opaque; they never grant access to another session.
func projectSessionGoal(ctx context.Context, q *agentdb.Queries, stream agentdb.LockTaskEventStreamRow, event domain.Event, now time.Time) error {
	var input agentv1.AgentTaskInput
	var envelope agentv1.AgentEvent
	if protojson.Unmarshal(stream.Input, &input) != nil || input.GetAgentSessionId() == "" || protojson.Unmarshal(event.Payload, &envelope) != nil {
		return domain.ErrInvalid
	}
	value := envelope.GetGoalUpdated().GetGoal()
	if value == nil || value.GetPauseRequested() {
		return domain.ErrInvalid
	}
	goal := domain.GoalProjection{Ref: value.Ref, Revision: value.Revision, Objective: value.Objective, Phase: value.Phase, RoundsStarted: value.RoundsStarted, MaxRounds: value.MaxRounds, BlockedReason: value.BlockedReason, Armed: value.Armed}
	if err := goal.Validate(); err != nil {
		return err
	}
	row, err := q.GetAgentSession(ctx, agentdb.GetAgentSessionParams{OwnerUserID: stream.OwnerUserID, SessionID: input.AgentSessionId})
	if err != nil {
		return sessionError("project goal session", err)
	}
	if uuidString(row.ActiveTaskID) != stream.ID {
		return domain.ErrLeaseLost
	}
	previous, err := domain.DecodeGoalProjection(row.GoalProjection)
	if err != nil {
		return err
	}
	if previous != nil {
		if previous.Ref != goal.Ref && previous.Phase != "complete" {
			return domain.ErrInvalid
		}
		if previous.Ref == goal.Ref && previous.Phase == "complete" && goal.Phase != "complete" {
			return domain.ErrInvalid
		}
		if previous.Ref == goal.Ref && previous.Revision == goal.Revision && (previous.Objective != goal.Objective || previous.Phase != goal.Phase || previous.MaxRounds != goal.MaxRounds || previous.BlockedReason != goal.BlockedReason) {
			return domain.ErrInvalid
		}
		if previous.Ref == goal.Ref && (goal.Revision < previous.Revision || goal.RoundsStarted < previous.RoundsStarted) {
			return domain.ErrInvalid
		}
	}
	encoded, err := json.Marshal(goal)
	if err != nil {
		return err
	}
	task, err := requiredUUID(stream.ID)
	if err != nil {
		return err
	}
	n, err := q.ProjectSessionGoal(ctx, agentdb.ProjectSessionGoalParams{OwnerUserID: stream.OwnerUserID, SessionID: input.AgentSessionId, ActiveTaskID: task, GoalProjection: encoded, UpdatedAt: timestamp(now)})
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.ErrLeaseLost
	}
	return nil
}

func disarmTaskAutomation(ctx context.Context, q *agentdb.Queries, task string) error {
	id, err := requiredUUID(task)
	if err != nil {
		return err
	}
	if err := q.DisarmSessionGoal(ctx, id); err != nil {
		return err
	}
	return q.ReviewInterruptedDelegations(ctx, task)
}
