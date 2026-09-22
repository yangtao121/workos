package postgres

import (
	"context"
	"errors"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/core/agent/adapters/postgres/agentdb"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"google.golang.org/protobuf/encoding/protojson"
)

func delegationFromRow(d agentdb.WorkosCoreAgentDelegation) domain.Delegation {
	return domain.Delegation{ID: d.ID, OwnerUserID: d.OwnerUserID, SessionID: d.SessionID, TaskID: d.TaskID, Key: d.IdempotencyKey, Title: d.Title, BindingID: d.BindingID, BindingRevision: d.BindingRevision, SourceID: d.SourceID, State: d.State, WorktreeID: d.WorktreeID, BaseCommit: d.BaseCommit, ResultSummary: d.ResultSummary, ResultArtifactID: d.ResultArtifactID}
}

func lockDelegationScope(ctx context.Context, q *agentdb.Queries, lease, worker string, now time.Time) (agentdb.LockTaskEventStreamRow, agentdb.WorkosCoreAgentSession, error) {
	id, err := requiredUUID(lease)
	if err != nil {
		return agentdb.LockTaskEventStreamRow{}, agentdb.WorkosCoreAgentSession{}, domain.ErrLeaseLost
	}
	stream, err := q.LockTaskEventStream(ctx, agentdb.LockTaskEventStreamParams{LeaseID: id, LockedBy: text(worker), LockedUntil: timestamp(now)})
	if err != nil || domain.State(stream.State).Terminal() {
		return stream, agentdb.WorkosCoreAgentSession{}, domain.ErrLeaseLost
	}
	var input agentv1.AgentTaskInput
	if protojson.Unmarshal(stream.Input, &input) != nil || input.GetAgentSessionId() == "" {
		return stream, agentdb.WorkosCoreAgentSession{}, domain.ErrInvalid
	}
	session, err := q.GetAgentSession(ctx, agentdb.GetAgentSessionParams{OwnerUserID: stream.OwnerUserID, SessionID: input.AgentSessionId})
	if err != nil || uuidString(session.ActiveTaskID) != stream.ID || session.ProjectID != streamProjectIDString(stream.ProjectID) {
		return stream, session, domain.ErrProjectDenied
	}
	return stream, session, nil
}

func (r *Repository) AcquireDelegation(ctx context.Context, lease, worker string, request domain.Delegation, now time.Time) (domain.Delegation, error) {
	if request.Key == "" || len(request.Key) > 128 || strings.TrimSpace(request.Title) == "" || len(request.Title) > 256 || !utf8.ValidString(request.Title) {
		return domain.Delegation{}, domain.ErrInvalid
	}
	if request.SourceID == "" || len(request.SourceID) > 256 || !utf8.ValidString(request.SourceID) {
		return domain.Delegation{}, domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Delegation{}, err
	}
	defer tx.Rollback(ctx)
	q := r.queries.WithTx(tx)
	stream, session, err := lockDelegationScope(ctx, q, lease, worker, now)
	if err != nil {
		return domain.Delegation{}, err
	}
	if request.BindingID != uuidString(session.WorkspaceBindingID) || request.BindingRevision != session.WorkspaceBindingRevision {
		return domain.Delegation{}, domain.ErrProjectDenied
	}
	row, err := q.GetAgentDelegationByKey(ctx, agentdb.GetAgentDelegationByKeyParams{TaskID: stream.ID, IdempotencyKey: request.Key})
	if err == nil {
		saved := delegationFromRow(row)
		if saved.Title != request.Title || saved.SourceID != request.SourceID || saved.BindingID != request.BindingID || saved.BindingRevision != request.BindingRevision {
			return domain.Delegation{}, domain.ErrSessionInputConflict
		}
		return saved, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Delegation{}, err
	}
	count, err := q.CountRunningDelegations(ctx, stream.ID)
	if err != nil {
		return domain.Delegation{}, err
	}
	if count >= 2 {
		return domain.Delegation{}, domain.ErrSessionBusy
	}
	id, err := uuid.NewV7()
	if err != nil {
		return domain.Delegation{}, err
	}
	request.ID, request.OwnerUserID, request.SessionID, request.TaskID, request.State = id.String(), stream.OwnerUserID, session.SessionID, stream.ID, "preparing"
	if err := q.InsertAgentDelegation(ctx, agentdb.InsertAgentDelegationParams{ID: request.ID, OwnerUserID: request.OwnerUserID, SessionID: request.SessionID, TaskID: request.TaskID, IdempotencyKey: request.Key, Title: request.Title, BindingID: request.BindingID, BindingRevision: request.BindingRevision, SourceID: request.SourceID, CreatedAt: timestamp(now)}); err != nil {
		return domain.Delegation{}, err
	}
	if err := appendDelegationEvent(ctx, q, stream, request, now); err != nil {
		return domain.Delegation{}, err
	}
	return request, tx.Commit(ctx)
}

func (r *Repository) GetTaskDelegation(ctx context.Context, lease, worker, id string, now time.Time) (domain.Delegation, error) {
	if _, err := requiredUUID(id); err != nil {
		return domain.Delegation{}, domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Delegation{}, err
	}
	defer tx.Rollback(ctx)
	q := r.queries.WithTx(tx)
	stream, _, err := lockDelegationScope(ctx, q, lease, worker, now)
	if err != nil {
		return domain.Delegation{}, err
	}
	row, err := q.GetAgentDelegation(ctx, agentdb.GetAgentDelegationParams{ID: id, OwnerUserID: stream.OwnerUserID, TaskID: stream.ID})
	if err != nil {
		return domain.Delegation{}, storeError("get task delegation", err)
	}
	return delegationFromRow(row), tx.Commit(ctx)
}

func (r *Repository) UpdateTaskDelegation(ctx context.Context, lease, worker string, update domain.Delegation, now time.Time) (domain.Delegation, error) {
	if _, err := requiredUUID(update.ID); err != nil {
		return domain.Delegation{}, domain.ErrInvalid
	}
	if update.State != "running" && !update.Terminal() {
		return domain.Delegation{}, domain.ErrInvalid
	}
	if len(update.ResultSummary) > 2048 || !utf8.ValidString(update.ResultSummary) || len(update.BaseCommit) > 64 || len(update.WorktreeID) > 128 || len(update.ResultArtifactID) > 128 {
		return domain.Delegation{}, domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Delegation{}, err
	}
	defer tx.Rollback(ctx)
	q := r.queries.WithTx(tx)
	stream, _, err := lockDelegationScope(ctx, q, lease, worker, now)
	if err != nil {
		return domain.Delegation{}, err
	}
	row, err := q.GetAgentDelegation(ctx, agentdb.GetAgentDelegationParams{ID: update.ID, OwnerUserID: stream.OwnerUserID, TaskID: stream.ID})
	if err != nil {
		return domain.Delegation{}, storeError("update task delegation", err)
	}
	saved := delegationFromRow(row)
	if saved.State == update.State && saved.WorktreeID == update.WorktreeID && saved.BaseCommit == update.BaseCommit && saved.ResultSummary == update.ResultSummary && saved.ResultArtifactID == update.ResultArtifactID {
		return saved, tx.Commit(ctx)
	}
	if saved.Terminal() || (update.State == "completed" && saved.State != "running") {
		return domain.Delegation{}, domain.ErrSessionInputConflict
	}
	if saved.WorktreeID != "" && (saved.WorktreeID != update.WorktreeID || saved.BaseCommit != update.BaseCommit) {
		return domain.Delegation{}, domain.ErrInvalid
	}
	if update.State == "running" && (update.WorktreeID != saved.ID || update.BaseCommit == "") {
		return domain.Delegation{}, domain.ErrInvalid
	}
	n, err := q.UpdateAgentDelegation(ctx, agentdb.UpdateAgentDelegationParams{ID: saved.ID, OwnerUserID: saved.OwnerUserID, TaskID: saved.TaskID, State: update.State, WorktreeID: update.WorktreeID, BaseCommit: update.BaseCommit, ResultSummary: update.ResultSummary, ResultArtifactID: update.ResultArtifactID, UpdatedAt: timestamp(now)})
	if err != nil {
		return domain.Delegation{}, err
	}
	if n != 1 {
		return domain.Delegation{}, domain.ErrSessionInputConflict
	}
	saved.State, saved.WorktreeID, saved.BaseCommit, saved.ResultSummary, saved.ResultArtifactID = update.State, update.WorktreeID, update.BaseCommit, update.ResultSummary, update.ResultArtifactID
	if err := appendDelegationEvent(ctx, q, stream, saved, now); err != nil {
		return domain.Delegation{}, err
	}
	return saved, tx.Commit(ctx)
}

func appendDelegationEvent(ctx context.Context, q *agentdb.Queries, stream agentdb.LockTaskEventStreamRow, d domain.Delegation, now time.Time) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	payload, err := protojson.Marshal(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_DelegationUpdated{DelegationUpdated: &agentv1.DelegationUpdated{Delegation: &agentv1.AgentDelegation{Id: d.ID, TaskId: d.TaskID, Title: d.Title, State: d.State, WorktreeId: d.WorktreeID, BaseCommit: d.BaseCommit, ResultSummary: d.ResultSummary, ResultArtifactId: d.ResultArtifactID}}}})
	if err != nil {
		return err
	}
	event := domain.Event{ID: id.String(), TaskID: stream.ID, Sequence: stream.LastEventSequence + 1, EventType: "delegation_updated", Payload: payload, OccurredAt: now}
	if err := addEventMetadata(&event); err != nil {
		return err
	}
	if err := q.AdvanceTaskState(ctx, agentdb.AdvanceTaskStateParams{State: stream.State, TaskID: stream.ID, Sequence: event.Sequence, UpdatedAt: timestamp(now)}); err != nil {
		return err
	}
	return insertEvent(ctx, q, event)
}

func (r *Repository) AuthorizeDelegationPublication(ctx context.Context, tx dbtx.Tx, stream agentports.TaskStreamFacts, id string) error {
	child, err := r.queries.WithTx(tx).GetAgentDelegation(ctx, agentdb.GetAgentDelegationParams{ID: id, OwnerUserID: stream.OwnerUserID, TaskID: stream.TaskID})
	if err != nil || child.State != "running" {
		return domain.ErrProjectDenied
	}
	return nil
}
