package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/core/agent/adapters/postgres/agentdb"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/agent/ports"
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *agentdb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: agentdb.New(pool)}
}

func (r *Repository) Create(ctx context.Context, task domain.Task, idempotencyKey string) (domain.Task, error) {
	projectID, err := nullableUUID(task.ProjectID)
	if err != nil {
		return domain.Task{}, domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Task{}, fmt.Errorf("begin create task: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	rows, err := queries.InsertAgentTask(ctx, agentdb.InsertAgentTaskParams{
		ID: task.ID, OwnerUserID: task.OwnerUserID, IdempotencyKey: idempotencyKey,
		ProjectID: projectID, Input: task.Input, State: string(task.State), ProviderID: task.ProviderID,
		CreatedAt: timestamp(task.CreatedAt), UpdatedAt: timestamp(task.UpdatedAt),
	})
	if err != nil {
		return domain.Task{}, fmt.Errorf("insert task: %w", err)
	}
	if rows == 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.Task{}, fmt.Errorf("commit idempotent task: %w", err)
		}
		return r.GetByIdempotency(ctx, task.OwnerUserID, idempotencyKey)
	}
	payload, err := json.Marshal(map[string]string{"taskId": task.ID})
	if err != nil {
		return domain.Task{}, fmt.Errorf("encode task request: %w", err)
	}
	outboxID, err := uuid.NewV7()
	if err != nil {
		return domain.Task{}, fmt.Errorf("generate task outbox id: %w", err)
	}
	if err := queries.InsertTaskOutbox(ctx, agentdb.InsertTaskOutboxParams{
		ID: outboxID.String(), AggregateID: task.ID, Payload: payload, OccurredAt: timestamp(task.CreatedAt),
	}); err != nil {
		return domain.Task{}, fmt.Errorf("append task outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Task{}, fmt.Errorf("commit task: %w", err)
	}
	return task, nil
}

func (r *Repository) Get(ctx context.Context, ownerID, taskID string) (domain.Task, error) {
	value, err := r.queries.GetAgentTask(ctx, agentdb.GetAgentTaskParams{OwnerUserID: ownerID, ID: taskID})
	return taskFromDB(value, err)
}

// CreateForApp inserts the task, its App provenance mapping, and the task
// outbox row in one transaction. Same-key races are arbitrated by the mapping
// primary key: the loser re-reads the consumed mapping and replays the
// winner's task exactly — or aborts — while its own partially created rows
// roll back, so no orphan task, mapping, or outbox row can survive.
func (r *Repository) CreateForApp(ctx context.Context, task domain.Task, provenance ports.AppTaskProvenance) (domain.Task, error) {
	projectID, err := nullableUUID(task.ProjectID)
	if err != nil {
		return domain.Task{}, domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Task{}, fmt.Errorf("begin create app task: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	if _, err := queries.InsertAgentTask(ctx, agentdb.InsertAgentTaskParams{
		ID: task.ID, OwnerUserID: task.OwnerUserID, IdempotencyKey: provenance.TaskIdempotencyKey,
		ProjectID: projectID, Input: task.Input, State: string(task.State), ProviderID: task.ProviderID,
		CreatedAt: timestamp(task.CreatedAt), UpdatedAt: timestamp(task.UpdatedAt),
	}); err != nil {
		return domain.Task{}, fmt.Errorf("insert app task: %w", err)
	}
	rows, err := queries.InsertAgentAppTaskRequest(ctx, agentdb.InsertAgentAppTaskRequestParams{
		OwnerUserID: task.OwnerUserID, AppInstanceID: provenance.AppInstanceID,
		ClientIdempotencyKey: provenance.ClientIdempotencyKey, RequestDigest: provenance.RequestDigest,
		TaskID: task.ID, ProjectID: task.ProjectID, CreatedAt: timestamp(task.CreatedAt),
	})
	if err != nil {
		return domain.Task{}, fmt.Errorf("insert app task mapping: %w", err)
	}
	if rows == 0 {
		consumed, err := queries.GetAgentAppTaskRequest(ctx, agentdb.GetAgentAppTaskRequestParams{
			OwnerUserID: task.OwnerUserID, AppInstanceID: provenance.AppInstanceID,
			ClientIdempotencyKey: provenance.ClientIdempotencyKey,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Task{}, fmt.Errorf("app task mapping vanished mid-transaction: %w", domain.ErrInvalid)
		}
		if err != nil {
			return domain.Task{}, fmt.Errorf("classify app task mapping: %w", err)
		}
		if consumed.RequestDigest != provenance.RequestDigest {
			return domain.Task{}, domain.ErrIdempotencyConflict
		}
		value, err := queries.GetAgentTask(ctx, agentdb.GetAgentTaskParams{OwnerUserID: task.OwnerUserID, ID: consumed.TaskID})
		winner, err := taskFromDB(value, err)
		if err != nil {
			return domain.Task{}, err
		}
		return winner, nil
	}
	payload, err := json.Marshal(map[string]string{"taskId": task.ID})
	if err != nil {
		return domain.Task{}, fmt.Errorf("encode task request: %w", err)
	}
	outboxID, err := uuid.NewV7()
	if err != nil {
		return domain.Task{}, fmt.Errorf("generate task outbox id: %w", err)
	}
	if err := queries.InsertTaskOutbox(ctx, agentdb.InsertTaskOutboxParams{
		ID: outboxID.String(), AggregateID: task.ID, Payload: payload, OccurredAt: timestamp(task.CreatedAt),
	}); err != nil {
		return domain.Task{}, fmt.Errorf("append task outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Task{}, fmt.Errorf("commit app task: %w", err)
	}
	return task, nil
}

// GetAppTaskRequest reads one consumed (owner, app instance, client key)
// mapping for replay adjudication.
func (r *Repository) GetAppTaskRequest(ctx context.Context, ownerID, appInstanceID, clientKey string) (ports.AppTaskRequestRecord, bool, error) {
	value, err := r.queries.GetAgentAppTaskRequest(ctx, agentdb.GetAgentAppTaskRequestParams{
		OwnerUserID: ownerID, AppInstanceID: appInstanceID, ClientIdempotencyKey: clientKey,
	})
	return appTaskRequestRecord(value, err)
}

// GetAppTaskByTask reads the mapping proving one task was created by one app
// installation of one owner.
func (r *Repository) GetAppTaskByTask(ctx context.Context, ownerID, appInstanceID, taskID string) (ports.AppTaskRequestRecord, bool, error) {
	value, err := r.queries.GetAgentAppTaskByTask(ctx, agentdb.GetAgentAppTaskByTaskParams{
		OwnerUserID: ownerID, AppInstanceID: appInstanceID, TaskID: taskID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.AppTaskRequestRecord{}, false, nil
	}
	if err != nil {
		return ports.AppTaskRequestRecord{}, false, fmt.Errorf("query app task mapping: %w", err)
	}
	return ports.AppTaskRequestRecord{
		RequestDigest: value.RequestDigest, TaskID: value.TaskID, ProjectID: value.ProjectID,
	}, true, nil
}

func appTaskRequestRecord(value agentdb.GetAgentAppTaskRequestRow, err error) (ports.AppTaskRequestRecord, bool, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.AppTaskRequestRecord{}, false, nil
	}
	if err != nil {
		return ports.AppTaskRequestRecord{}, false, fmt.Errorf("query app task mapping: %w", err)
	}
	return ports.AppTaskRequestRecord{
		RequestDigest: value.RequestDigest, TaskID: value.TaskID, ProjectID: value.ProjectID,
	}, true, nil
}

func (r *Repository) GetByIdempotency(ctx context.Context, ownerID, key string) (domain.Task, error) {
	value, err := r.queries.GetAgentTaskByIdempotency(ctx, agentdb.GetAgentTaskByIdempotencyParams{
		OwnerUserID: ownerID, IdempotencyKey: key,
	})
	return taskFromDB(value, err)
}

func (r *Repository) List(ctx context.Context, ownerID, projectID, cursor string, limit int) ([]domain.Task, error) {
	if cursor == "" {
		cursor = "00000000-0000-0000-0000-000000000000"
	}
	values, err := r.queries.ListAgentTasks(ctx, agentdb.ListAgentTasksParams{
		OwnerUserID: ownerID, ProjectID: projectID, Cursor: cursor, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	result := make([]domain.Task, 0, len(values))
	for _, value := range values {
		task, err := taskFromDB(value, nil)
		if err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, nil
}

func (r *Repository) Cancel(ctx context.Context, ownerID, taskID, reason string, now time.Time) (domain.Task, *domain.Event, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Task{}, nil, fmt.Errorf("begin cancel task: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	value, err := queries.GetAgentTaskForUpdate(ctx, agentdb.GetAgentTaskForUpdateParams{OwnerUserID: ownerID, ID: taskID})
	task, err := taskFromDB(value, err)
	if err != nil {
		return domain.Task{}, nil, err
	}
	if task.State.Terminal() {
		if err := tx.Commit(ctx); err != nil {
			return domain.Task{}, nil, fmt.Errorf("commit terminal cancellation: %w", err)
		}
		return task, nil, nil
	}
	task.State, task.CancellationRequested, task.UpdatedAt = domain.StateCancelled, true, now
	task.LastEventSequence++
	eventID, err := uuid.NewV7()
	if err != nil {
		return domain.Task{}, nil, fmt.Errorf("generate cancellation event id: %w", err)
	}
	payload, err := json.Marshal(map[string]any{"runCancelled": map[string]string{"reason": reason}})
	if err != nil {
		return domain.Task{}, nil, fmt.Errorf("encode cancellation event: %w", err)
	}
	event := domain.Event{ID: eventID.String(), TaskID: task.ID, Sequence: task.LastEventSequence, EventType: "run_cancelled", Payload: payload, OccurredAt: now}
	if err := addEventMetadata(&event); err != nil {
		return domain.Task{}, nil, err
	}
	if err := queries.MarkTaskCancelled(ctx, agentdb.MarkTaskCancelledParams{
		LastEventSequence: event.Sequence, UpdatedAt: timestamp(now), ID: task.ID,
	}); err != nil {
		return domain.Task{}, nil, fmt.Errorf("cancel task: %w", err)
	}
	if err := insertEvent(ctx, queries, event); err != nil {
		return domain.Task{}, nil, err
	}
	if err := queries.FinishPendingTaskRequest(ctx, agentdb.FinishPendingTaskRequestParams{
		ProcessedAt: timestamp(now), AggregateID: task.ID,
	}); err != nil {
		return domain.Task{}, nil, fmt.Errorf("finish cancelled task request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Task{}, nil, fmt.Errorf("commit cancellation: %w", err)
	}
	return task, &event, nil
}

func (r *Repository) ListEvents(ctx context.Context, ownerID, taskID string, after int64, limit int) ([]domain.Event, error) {
	allowed, err := r.queries.TaskBelongsToOwner(ctx, agentdb.TaskBelongsToOwnerParams{ID: taskID, OwnerUserID: ownerID})
	if err != nil {
		return nil, fmt.Errorf("authorize event stream: %w", err)
	}
	if !allowed {
		return nil, domain.ErrNotFound
	}
	values, err := r.queries.ListTaskEvents(ctx, agentdb.ListTaskEventsParams{
		StreamID: taskID, Sequence: after, Limit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list task events: %w", err)
	}
	result := make([]domain.Event, 0, len(values))
	for _, value := range values {
		result = append(result, domain.Event{
			ID: value.ID, TaskID: value.StreamID, Sequence: value.Sequence, EventType: value.EventType,
			Payload: value.Payload, OccurredAt: value.OccurredAt.Time,
		})
	}
	return result, nil
}

func (r *Repository) Claim(ctx context.Context, workerID string, duration time.Duration, leaseID string, now time.Time) (*domain.Lease, error) {
	leaseUUID, err := requiredUUID(leaseID)
	if err != nil {
		return nil, domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin task claim: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	taskID, err := queries.SelectTaskClaim(ctx, timestamp(now))
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit empty task claim: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select task claim: %w", err)
	}
	expires := now.Add(duration)
	if err := queries.LeaseTask(ctx, agentdb.LeaseTaskParams{
		LeaseID: leaseUUID, LockedBy: text(workerID), LockedUntil: timestamp(expires), AggregateID: taskID,
	}); err != nil {
		return nil, fmt.Errorf("lease task: %w", err)
	}
	if err := queries.MarkTaskRunning(ctx, agentdb.MarkTaskRunningParams{UpdatedAt: timestamp(now), ID: taskID}); err != nil {
		return nil, fmt.Errorf("mark task running: %w", err)
	}
	value, err := queries.GetAgentTaskUnscoped(ctx, taskID)
	task, err := taskFromDB(value, err)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit task claim: %w", err)
	}
	return &domain.Lease{ID: leaseID, WorkerID: workerID, Task: task, ExpiresAt: expires}, nil
}

func (r *Repository) Renew(ctx context.Context, leaseID, workerID string, duration time.Duration, now time.Time) (time.Time, bool, error) {
	leaseUUID, err := requiredUUID(leaseID)
	if err != nil {
		return time.Time{}, false, domain.ErrLeaseLost
	}
	expires := now.Add(duration)
	cancelled, err := r.queries.RenewTaskLease(ctx, agentdb.RenewTaskLeaseParams{
		ExpiresAt: timestamp(expires), LeaseID: leaseUUID, WorkerID: text(workerID), ObservedAt: timestamp(now),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, domain.ErrLeaseLost
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("renew task lease: %w", err)
	}
	return expires, cancelled, nil
}

func (r *Repository) AppendEvent(ctx context.Context, leaseID, workerID string, event domain.Event, state domain.State, providerID, runID string, now time.Time) (domain.Event, error) {
	leaseUUID, err := requiredUUID(leaseID)
	if err != nil {
		return domain.Event{}, domain.ErrLeaseLost
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Event{}, fmt.Errorf("begin append event: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	stream, err := queries.LockTaskEventStream(ctx, agentdb.LockTaskEventStreamParams{
		LeaseID: leaseUUID, LockedBy: text(workerID), LockedUntil: timestamp(now),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Event{}, domain.ErrLeaseLost
	}
	if err != nil {
		return domain.Event{}, fmt.Errorf("lock task event stream: %w", err)
	}
	if domain.State(stream.State).Terminal() {
		return domain.Event{}, domain.ErrTerminal
	}
	if event.EventType == "run_started" && (providerID == "" || providerID != stream.ProviderID) {
		return domain.Event{}, domain.ErrProviderMismatch
	}
	event.TaskID, event.Sequence = stream.ID, stream.LastEventSequence+1
	if err := addEventMetadata(&event); err != nil {
		return domain.Event{}, err
	}
	if err := queries.AdvanceTaskState(ctx, agentdb.AdvanceTaskStateParams{
		State: string(state), RunID: runID, Sequence: event.Sequence,
		UpdatedAt: timestamp(now), TaskID: event.TaskID,
	}); err != nil {
		return domain.Event{}, fmt.Errorf("advance task state: %w", err)
	}
	if err := insertEvent(ctx, queries, event); err != nil {
		return domain.Event{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Event{}, fmt.Errorf("commit task event: %w", err)
	}
	return event, nil
}

func (r *Repository) FinishLease(ctx context.Context, leaseID, workerID string, now time.Time) error {
	leaseUUID, err := requiredUUID(leaseID)
	if err != nil {
		return domain.ErrLeaseLost
	}
	rows, err := r.queries.FinishTaskLease(ctx, agentdb.FinishTaskLeaseParams{
		ProcessedAt: timestamp(now), LeaseID: leaseUUID, LockedBy: text(workerID),
	})
	if err != nil {
		return fmt.Errorf("finish task lease: %w", err)
	}
	if rows == 0 {
		return domain.ErrLeaseLost
	}
	return nil
}

func taskFromDB(value agentdb.WorkosCoreAgentTask, err error) (domain.Task, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Task{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Task{}, fmt.Errorf("query agent task: %w", err)
	}
	projectID := ""
	if value.ProjectID.Valid {
		projectID = uuid.UUID(value.ProjectID.Bytes).String()
	}
	return domain.Task{
		ID: value.ID, OwnerUserID: value.OwnerUserID, ProjectID: projectID, Input: value.Input,
		State: domain.State(value.State), ProviderID: value.ProviderID, HarnessInstanceID: value.HarnessInstanceID,
		RunID: value.RunID, LastEventSequence: value.LastEventSequence,
		CancellationRequested: value.CancellationRequested, CreatedAt: value.CreatedAt.Time, UpdatedAt: value.UpdatedAt.Time,
	}, nil
}

func insertEvent(ctx context.Context, queries *agentdb.Queries, event domain.Event) error {
	if err := queries.InsertTaskEvent(ctx, agentdb.InsertTaskEventParams{
		ID: event.ID, StreamID: event.TaskID, Sequence: event.Sequence, EventType: event.EventType,
		Payload: event.Payload, OccurredAt: timestamp(event.OccurredAt),
	}); err != nil {
		return fmt.Errorf("insert task event: %w", err)
	}
	return nil
}

func nullableUUID(value string) (pgtype.UUID, error) {
	if value == "" {
		return pgtype.UUID{}, nil
	}
	return requiredUUID(value)
}

func requiredUUID(value string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: [16]byte(parsed), Valid: true}, nil
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

func addEventMetadata(event *domain.Event) error {
	var document map[string]any
	if err := json.Unmarshal(event.Payload, &document); err != nil {
		return fmt.Errorf("decode canonical event payload: %w", err)
	}
	document["id"] = event.ID
	document["taskId"] = event.TaskID
	document["sequence"] = event.Sequence
	document["occurredAt"] = event.OccurredAt.Format(time.RFC3339Nano)
	payload, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode canonical event payload: %w", err)
	}
	event.Payload = payload
	return nil
}
