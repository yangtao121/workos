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
	notificationdomain "github.com/yangtao121/workos/internal/core/notification/domain"
	notificationports "github.com/yangtao121/workos/internal/core/notification/ports"
	"github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// storeError wraps a storage failure at the port boundary. Transient
// dependency failures (unreachable server, broken connection, resource
// exhaustion) carry the ErrStoreUnavailable sentinel so transports can answer
// a sanitized retryable Unavailable; every other failure stays an opaque
// internal error — classification never reads SQLSTATE message text or
// constraint names.
func storeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", operation, ports.ErrStoreUnavailable, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

type Repository struct {
	pool    *pgxpool.Pool
	queries *agentdb.Queries
	// notifications is the tx-scoped notification projection sink
	// (ADR-0014). It is wired by the composition root; every notification-
	// bearing producer path fails closed without it, never skips silently.
	notifications notificationports.TxSink
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: agentdb.New(pool)}
}

// NewWithNotificationSink wires the composition-root sink: terminal and
// approval notifications are a hard requirement of their source
// transactions, so a nil sink is a construction failure.
func NewWithNotificationSink(pool *pgxpool.Pool, notifications notificationports.TxSink) (*Repository, error) {
	if notifications == nil {
		return nil, errors.New("agent repository requires the notification sink")
	}
	return &Repository{pool: pool, queries: agentdb.New(pool), notifications: notifications}, nil
}

// appendNotificationTx projects one notification inside the caller's source
// transaction; the returned error must roll the source mutation back
// (ADR-0014 hard requirement).
func (r *Repository) appendNotificationTx(ctx context.Context, tx dbtx.Tx, fact notificationdomain.SystemFact, occurredAt time.Time) error {
	if r.notifications == nil {
		return errors.New("notification sink is not wired")
	}
	if _, err := r.notifications.AppendSystemNotification(ctx, tx, fact, occurredAt); err != nil {
		return fmt.Errorf("append notification: %w", err)
	}
	return nil
}

// taskTerminalNotificationFact prepares the finite task-terminal fact for a
// task that first reached a terminal state in this transaction.
func taskTerminalNotificationFact(ownerUserID, projectID, taskID, category string) (notificationdomain.SystemFact, error) {
	if !notificationdomain.ValidUUID(taskID) {
		return notificationdomain.SystemFact{}, domain.ErrInvalid
	}
	return notificationdomain.SystemFact{
		Kind: notificationdomain.KindAgentTaskTerminal, OwnerUserID: ownerUserID,
		ProjectID: projectID, Category: category,
		TargetID: taskID, SourceID: taskID,
	}, nil
}

// streamProjectIDString projects the lock row's nullable project binding.
func streamProjectIDString(projectID pgtype.UUID) string {
	if !projectID.Valid {
		return ""
	}
	return projectID.String()
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
		// User-submitted tasks carry no policy enforcement in this slice: the
		// snapshot columns stay NULL (no fabricated history).
		PolicySource: pgtype.Text{}, PolicyRevision: pgtype.Int8{}, PolicySpecDigest: pgtype.Text{},
		BudgetMaxOutputTokens: pgtype.Int8{}, BudgetMaxRuntimeSeconds: pgtype.Int8{},
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
		// The outbox append is a storage step like any other: a transient
		// PostgreSQL failure here must carry the ErrStoreUnavailable
		// sentinel so transports answer sanitized Unavailable, never a raw
		// Internal with database detail.
		return domain.Task{}, storeError("append task outbox", err)
	}
	if err := insertTaskCredentialSnapshot(ctx, queries, task); err != nil {
		return domain.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Task{}, fmt.Errorf("commit task: %w", err)
	}
	return task, nil
}

func (r *Repository) Get(ctx context.Context, ownerID, taskID string) (domain.Task, error) {
	value, err := r.queries.GetAgentTask(ctx, agentdb.GetAgentTaskParams{OwnerUserID: ownerID, ID: taskID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Task{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Task{}, storeError("query agent task", err)
	}
	task, err := taskFromDB(value, nil)
	if err != nil {
		return domain.Task{}, err
	}
	if err := r.attachCredentialSnapshot(ctx, r.queries, &task); err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

// insertTaskCredentialSnapshot persists the durable credential snapshot in
// the task's own transaction. A task without a snapshot (providers that need
// no credential) writes nothing.
func insertTaskCredentialSnapshot(ctx context.Context, queries *agentdb.Queries, task domain.Task) error {
	if task.Credential == nil {
		return nil
	}
	if err := queries.InsertAgentTaskCredential(ctx, agentdb.InsertAgentTaskCredentialParams{
		TaskID: task.ID, ProviderID: task.ProviderID, CredentialID: task.Credential.CredentialID,
		CredentialRevision: task.Credential.Revision, CreatedAt: timestamp(task.CreatedAt),
	}); err != nil {
		return storeError("insert task credential snapshot", err)
	}
	return nil
}

// attachCredentialSnapshot loads the durable snapshot for one task so
// replay and claim paths verify history instead of re-resolving (ADR-0009).
// A missing row means the provider needed no credential.
func (r *Repository) attachCredentialSnapshot(ctx context.Context, queries *agentdb.Queries, task *domain.Task) error {
	row, err := queries.GetAgentTaskCredential(ctx, task.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return storeError("query task credential snapshot", err)
	}
	if row.ProviderID != task.ProviderID {
		// A snapshot bound to a different provider than the task row is a
		// stored-fact drift, not a replayable history.
		return fmt.Errorf("task credential snapshot provider drift: %w", domain.ErrInvalid)
	}
	task.Credential = &domain.CredentialSnapshot{CredentialID: row.CredentialID, Revision: row.CredentialRevision}
	return nil
}

// CreateForApp inserts the task, its App provenance mapping, the guarded
// daily quota reservation, and the task outbox row in one transaction
// (policy mode allow). Same-key races are arbitrated by the mapping primary
// key: the loser re-reads the consumed mapping and replays the winner's task
// exactly — or aborts — while its own partially created rows roll back, so no
// orphan task, mapping, reservation, or outbox row can survive. When the
// guarded reservation cannot fit the policy's UTC daily allowance the whole
// transaction fails with domain.ErrQuotaExhausted and the App run key is not
// consumed.
func (r *Repository) CreateForApp(ctx context.Context, task domain.Task, provenance ports.AppTaskProvenance, snapshot ports.PolicySnapshot, daily ports.DailyAllowance) (domain.Task, error) {
	projectID, err := nullableUUID(task.ProjectID)
	if err != nil {
		return domain.Task{}, domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Task{}, storeError("begin create app task", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	if _, err := queries.InsertAgentTask(ctx, agentdb.InsertAgentTaskParams{
		ID: task.ID, OwnerUserID: task.OwnerUserID, IdempotencyKey: provenance.TaskIdempotencyKey,
		ProjectID: projectID, Input: task.Input, State: string(task.State), ProviderID: task.ProviderID,
		CreatedAt: timestamp(task.CreatedAt), UpdatedAt: timestamp(task.UpdatedAt),
		PolicySource:            text(string(snapshot.Source)),
		PolicyRevision:          pgtype.Int8{Int64: snapshot.Revision, Valid: true},
		PolicySpecDigest:        text(snapshot.SpecDigest),
		BudgetMaxOutputTokens:   pgtype.Int8{Int64: snapshot.Spec.MaxOutputTokensPerTask, Valid: true},
		BudgetMaxRuntimeSeconds: pgtype.Int8{Int64: snapshot.Spec.MaxRuntimeSecondsPerTask, Valid: true},
	}); err != nil {
		return domain.Task{}, storeError("insert app task", err)
	}
	rows, err := queries.InsertAgentAppTaskRequest(ctx, agentdb.InsertAgentAppTaskRequestParams{
		OwnerUserID: task.OwnerUserID, AppInstanceID: provenance.AppInstanceID,
		ClientIdempotencyKey: provenance.ClientIdempotencyKey, RequestDigest: provenance.RequestDigest,
		TaskID: task.ID, ProjectID: task.ProjectID, CreatedAt: timestamp(task.CreatedAt),
	})
	if err != nil {
		return domain.Task{}, storeError("insert app task mapping", err)
	}
	if rows == 0 {
		winner, err := r.replayAppTaskMapping(ctx, queries, task.OwnerUserID, provenance)
		return winner, err
	}
	// Guarded daily reservation: the WHERE clause refuses the increment when
	// either ceiling would be crossed, so two concurrent Core instances can
	// never oversell the last slot — the loser's transaction rolls back with
	// no task, mapping, or reservation.
	if _, err := queries.ReserveAgentAppDailyQuota(ctx, agentdb.ReserveAgentAppDailyQuotaParams{
		OwnerUserID: task.OwnerUserID, AppInstanceID: provenance.AppInstanceID,
		UtcDate:              utcDate(task.CreatedAt),
		OutputTokensReserved: snapshot.Spec.MaxOutputTokensPerTask,
		PolicyRevision:       snapshot.Revision,
		CreatedAt:            timestamp(task.CreatedAt),
		MaxTasks:             daily.MaxTasks,
		MaxReservedTokens:    daily.MaxReservedOutputTokens,
	}); errors.Is(err, pgx.ErrNoRows) {
		return domain.Task{}, domain.ErrQuotaExhausted
	} else if err != nil {
		return domain.Task{}, storeError("reserve app task quota", err)
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
		// The outbox append is a storage step like any other: a transient
		// PostgreSQL failure here must carry the ErrStoreUnavailable
		// sentinel so transports answer sanitized Unavailable, never a raw
		// Internal with database detail.
		return domain.Task{}, storeError("append task outbox", err)
	}
	if err := insertTaskCredentialSnapshot(ctx, queries, task); err != nil {
		return domain.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Task{}, storeError("commit app task", err)
	}
	return task, nil
}

// replayAppTaskMapping classifies a lost same-key race: the winner's mapping
// digest either replays its task exactly or aborts this request. The caller's
// transaction (including any partially written task rows) rolls back.
func (r *Repository) replayAppTaskMapping(ctx context.Context, queries *agentdb.Queries, ownerID string, provenance ports.AppTaskProvenance) (domain.Task, error) {
	consumed, err := queries.GetAgentAppTaskRequest(ctx, agentdb.GetAgentAppTaskRequestParams{
		OwnerUserID: ownerID, AppInstanceID: provenance.AppInstanceID,
		ClientIdempotencyKey: provenance.ClientIdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Task{}, fmt.Errorf("app task mapping vanished mid-transaction: %w", domain.ErrInvalid)
	}
	if err != nil {
		return domain.Task{}, storeError("classify app task mapping", err)
	}
	if consumed.RequestDigest != provenance.RequestDigest {
		return domain.Task{}, domain.ErrIdempotencyConflict
	}
	value, err := queries.GetAgentTask(ctx, agentdb.GetAgentTaskParams{OwnerUserID: ownerID, ID: consumed.TaskID})
	task, err := taskFromDB(value, err)
	if err != nil {
		return domain.Task{}, err
	}
	if err := r.attachCredentialSnapshot(ctx, queries, &task); err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

// CreateForAppApproval inserts the waiting task, the App provenance mapping,
// the pending approval with its full policy-spec snapshot, and the
// approval_required event in one transaction. It deliberately creates no
// claimable outbox row and reserves no quota: a waiting task is not an
// enqueued task, and a rejected or expired approval never consumes a single
// reserved task or token. Inside the installation's policy-chain lock the
// approval's policy snapshot is re-verified against the stored fact, so a
// SetPolicy committing before this transaction is never shadowed by a stale
// pending approval, and one committing after it always sees and expires the
// approval.
func (r *Repository) CreateForAppApproval(ctx context.Context, task domain.Task, approval domain.Approval, provenance ports.AppTaskProvenance) (domain.Task, domain.Approval, error) {
	projectID, err := nullableUUID(task.ProjectID)
	if err != nil {
		return domain.Task{}, domain.Approval{}, domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Task{}, domain.Approval{}, storeError("begin create app approval", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	if err := lockAgentAppPolicyChain(ctx, queries, task.OwnerUserID, approval.AppInstanceID); err != nil {
		return domain.Task{}, domain.Approval{}, err
	}
	if err := verifyApprovalPolicyChain(ctx, queries, approval); err != nil {
		return domain.Task{}, domain.Approval{}, err
	}
	if _, err := queries.InsertAgentTask(ctx, agentdb.InsertAgentTaskParams{
		ID: task.ID, OwnerUserID: task.OwnerUserID, IdempotencyKey: provenance.TaskIdempotencyKey,
		ProjectID: projectID, Input: task.Input, State: string(task.State), ProviderID: task.ProviderID,
		CreatedAt: timestamp(task.CreatedAt), UpdatedAt: timestamp(task.UpdatedAt),
		PolicySource:            text(string(approval.Source)),
		PolicyRevision:          pgtype.Int8{Int64: approval.Revision, Valid: true},
		PolicySpecDigest:        text(approval.Spec.Digest()),
		BudgetMaxOutputTokens:   pgtype.Int8{Int64: approval.Spec.MaxOutputTokensPerTask, Valid: true},
		BudgetMaxRuntimeSeconds: pgtype.Int8{Int64: approval.Spec.MaxRuntimeSecondsPerTask, Valid: true},
	}); err != nil {
		return domain.Task{}, domain.Approval{}, storeError("insert app task", err)
	}
	rows, err := queries.InsertAgentAppTaskRequest(ctx, agentdb.InsertAgentAppTaskRequestParams{
		OwnerUserID: task.OwnerUserID, AppInstanceID: provenance.AppInstanceID,
		ClientIdempotencyKey: provenance.ClientIdempotencyKey, RequestDigest: provenance.RequestDigest,
		TaskID: task.ID, ProjectID: task.ProjectID, CreatedAt: timestamp(task.CreatedAt),
	})
	if err != nil {
		return domain.Task{}, domain.Approval{}, storeError("insert app task mapping", err)
	}
	if rows == 0 {
		winner, err := r.replayAppTaskMapping(ctx, queries, task.OwnerUserID, provenance)
		return winner, domain.Approval{}, err
	}
	if rows, err := queries.InsertAgentAppApproval(ctx, agentdb.InsertAgentAppApprovalParams{
		OwnerUserID: approval.OwnerUserID, ID: approval.ID, AppInstanceID: approval.AppInstanceID,
		ProjectID: approval.ProjectID, TaskID: approval.TaskID, AppID: approval.AppID,
		GoalExcerpt:                      approval.GoalExcerpt,
		ProviderID:                       approval.ProviderID,
		MaxOutputTokensPerTask:           approval.Spec.MaxOutputTokensPerTask,
		MaxRuntimeSecondsPerTask:         approval.Spec.MaxRuntimeSecondsPerTask,
		MaxTasksPerUtcDay:                approval.Spec.MaxTasksPerUTCDay,
		MaxReservedOutputTokensPerUtcDay: approval.Spec.MaxReservedOutputTokensPerUTCDay,
		PolicyRevision:                   approval.Revision, CreatedAt: timestamp(approval.CreatedAt),
	}); err != nil {
		return domain.Task{}, domain.Approval{}, storeError("insert app approval", err)
	} else if rows == 0 {
		return domain.Task{}, domain.Approval{}, fmt.Errorf("approval id collision: %w", domain.ErrInvalid)
	}
	if err := advanceTaskWithSystemEvent(ctx, queries, &task, task.State, "approval_required", approvalRequiredPayload(approval), task.CreatedAt); err != nil {
		return domain.Task{}, domain.Approval{}, err
	}
	// Owner notification for the pending approval (ADR-0014): the fact is a
	// hard requirement of this source transaction — a notification failure
	// rolls the whole waiting task back.
	if err := r.appendNotificationTx(ctx, tx, notificationdomain.SystemFact{
		Kind: notificationdomain.KindAgentApprovalRequired, OwnerUserID: task.OwnerUserID,
		ProjectID: task.ProjectID, TargetID: approval.ID,
		SourceID: approval.ID,
	}, task.CreatedAt); err != nil {
		return domain.Task{}, domain.Approval{}, err
	}
	if err := insertTaskCredentialSnapshot(ctx, queries, task); err != nil {
		return domain.Task{}, domain.Approval{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Task{}, domain.Approval{}, storeError("commit app approval", err)
	}
	approval.UpdatedAt = task.CreatedAt
	return task, approval, nil
}

// verifyApprovalPolicyChain re-checks, inside the policy-chain lock, that the
// approval still carries the installation's effective policy: the versioned
// system default only while no explicit row exists, and otherwise the exact
// stored revision, spec, and project binding. Any drift means a policy change
// the caller's pre-transaction read could not have seen — the verdict fails
// the whole transaction with nothing consumed.
func verifyApprovalPolicyChain(ctx context.Context, queries *agentdb.Queries, approval domain.Approval) error {
	value, err := queries.GetAgentAppPolicy(ctx, agentdb.GetAgentAppPolicyParams{
		OwnerUserID: approval.OwnerUserID, AppInstanceID: approval.AppInstanceID,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		defaultPolicy := domain.SystemDefaultPolicy()
		if approval.Source != domain.PolicySourceSystemDefault ||
			approval.Revision != defaultPolicy.Revision ||
			approval.Spec != defaultPolicy.Spec {
			return domain.ErrPolicyStale
		}
		return nil
	case err != nil:
		return storeError("query app policy", err)
	}
	policy, err := policyFromDB(value)
	if err != nil {
		return err
	}
	if policy.ProjectID != approval.ProjectID {
		return fmt.Errorf("policy project binding: %w", domain.ErrPolicyCorrupt)
	}
	if approval.Source != domain.PolicySourceExplicit ||
		policy.Revision != approval.Revision ||
		policy.Spec != approval.Spec {
		return domain.ErrPolicyStale
	}
	return nil
}

// advanceTaskWithSystemEvent advances the task's state and sequence and
// persists one Core-generated lifecycle event in the same transaction.
func advanceTaskWithSystemEvent(ctx context.Context, queries *agentdb.Queries, task *domain.Task, state domain.State, eventType string, payload json.RawMessage, occurredAt time.Time) error {
	task.LastEventSequence++
	if err := queries.AdvanceTaskState(ctx, agentdb.AdvanceTaskStateParams{
		State: string(state), RunID: "", Sequence: task.LastEventSequence,
		UpdatedAt: timestamp(occurredAt), TaskID: task.ID,
	}); err != nil {
		return fmt.Errorf("advance task state: %w", err)
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate system event id: %w", err)
	}
	event := domain.Event{ID: eventID.String(), TaskID: task.ID, Sequence: task.LastEventSequence, EventType: eventType, Payload: payload, OccurredAt: occurredAt}
	if err := addEventMetadata(&event); err != nil {
		return err
	}
	return insertEvent(ctx, queries, event)
}

// approvalRequiredPayload encodes the canonical approval_required event body.
func approvalRequiredPayload(approval domain.Approval) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{
		"approvalRequired": map[string]any{
			"approvalId":  approval.ID,
			"title":       "App task approval",
			"description": approval.GoalExcerpt,
		},
	})
	return payload
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
		return ports.AppTaskRequestRecord{}, false, storeError("query app task mapping by task", err)
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
		return ports.AppTaskRequestRecord{}, false, storeError("query app task mapping", err)
	}
	return ports.AppTaskRequestRecord{
		RequestDigest: value.RequestDigest, TaskID: value.TaskID, ProjectID: value.ProjectID,
	}, true, nil
}

func (r *Repository) GetByIdempotency(ctx context.Context, ownerID, key string) (domain.Task, error) {
	value, err := r.queries.GetAgentTaskByIdempotency(ctx, agentdb.GetAgentTaskByIdempotencyParams{
		OwnerUserID: ownerID, IdempotencyKey: key,
	})
	task, err := taskFromDB(value, err)
	if err != nil {
		return domain.Task{}, err
	}
	// Replay must return the first task's durable snapshot so the router
	// never re-adjudicates against a rotated or rebound credential.
	if err := r.attachCredentialSnapshot(ctx, r.queries, &task); err != nil {
		return domain.Task{}, err
	}
	return task, nil
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
	// A cancelled waiting task can no longer be decided: its pending approval
	// expires in the same transaction so the owner's list never shows a
	// decideable approval whose task is gone.
	if _, err := queries.ExpireTaskPendingApproval(ctx, agentdb.ExpireTaskPendingApprovalParams{
		UpdatedAt: timestamp(now), OwnerUserID: ownerID, TaskID: taskID,
	}); err != nil {
		return domain.Task{}, nil, fmt.Errorf("expire cancelled task approval: %w", err)
	}
	if err := queries.FinishPendingTaskRequest(ctx, agentdb.FinishPendingTaskRequestParams{
		ProcessedAt: timestamp(now), AggregateID: task.ID,
	}); err != nil {
		return domain.Task{}, nil, fmt.Errorf("finish cancelled task request: %w", err)
	}
	// Owner notification for the owner-driven terminal transition
	// (ADR-0014): a hard requirement of this source transaction.
	terminalFact, factErr := taskTerminalNotificationFact(ownerID, task.ProjectID, task.ID, string(domain.StateCancelled))
	if factErr != nil {
		return domain.Task{}, nil, factErr
	}
	if err := r.appendNotificationTx(ctx, tx, terminalFact, now); err != nil {
		return domain.Task{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Task{}, nil, fmt.Errorf("commit cancellation: %w", err)
	}
	return task, &event, nil
}

func (r *Repository) ListEvents(ctx context.Context, ownerID, taskID string, after int64, limit int) ([]domain.Event, error) {
	allowed, err := r.queries.TaskBelongsToOwner(ctx, agentdb.TaskBelongsToOwnerParams{ID: taskID, OwnerUserID: ownerID})
	if err != nil {
		return nil, storeError("authorize event stream", err)
	}
	if !allowed {
		return nil, domain.ErrNotFound
	}
	values, err := r.queries.ListTaskEvents(ctx, agentdb.ListTaskEventsParams{
		StreamID: taskID, Sequence: after, Limit: int32(limit),
	})
	if err != nil {
		return nil, storeError("list task events", err)
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
	// The claim path carries the snapshot existence so the worker knows to
	// derive its credential lease from this exact task lease (ADR-0009).
	if err := r.attachCredentialSnapshot(ctx, queries, &task); err != nil {
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

func (r *Repository) AppendEvent(ctx context.Context, leaseID, workerID string, event domain.Event, state domain.State, providerID, runID string, usage *domain.UsageReport, now time.Time) (domain.Event, error) {
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
	if usage != nil {
		if err := r.projectUsage(ctx, queries, tx, stream, *usage, now); err != nil {
			return domain.Event{}, err
		}
	}
	// Owner notification for the first terminal transition (ADR-0014): a
	// hard requirement of this source transaction. Late provider events and
	// terminal replays are refused above by the terminal state, so this
	// projects at most once per task.
	if state.Terminal() {
		fact, factErr := taskTerminalNotificationFact(stream.OwnerUserID, streamProjectIDString(stream.ProjectID), stream.ID, string(state))
		if factErr != nil {
			return domain.Event{}, factErr
		}
		if err := r.appendNotificationTx(ctx, tx, fact, now); err != nil {
			return domain.Event{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Event{}, fmt.Errorf("commit task event: %w", err)
	}
	return event, nil
}

// projectUsage accumulates one validated usage observation into the
// Agent-owned per-task and per-bucket usage facts inside the event's
// transaction. User-submitted tasks (no App provenance) have no bucket and
// are skipped. When the cumulative reported output crosses the task's
// reserved budget the bucket records an auditable breach and the task is
// deterministically flagged for cancellation — the worker's next lease
// renewal observes the flag and stops the run (ADR-0005 §6).
func (r *Repository) projectUsage(ctx context.Context, queries *agentdb.Queries, tx dbtx.Tx, stream agentdb.LockTaskEventStreamRow, usage domain.UsageReport, now time.Time) error {
	appInstanceID, err := queries.GetAgentAppTaskOwnerTask(ctx, agentdb.GetAgentAppTaskOwnerTaskParams{
		OwnerUserID: stream.OwnerUserID, TaskID: stream.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return storeError("resolve usage bucket", err)
	}
	var cost pgtype.Numeric
	if usage.CostDecimal != "" {
		if err := cost.Scan(usage.CostDecimal); err != nil {
			return fmt.Errorf("decode usage cost: %w", domain.ErrInvalid)
		}
	}
	accumulated, err := queries.UpsertAgentTaskUsage(ctx, agentdb.UpsertAgentTaskUsageParams{
		OwnerUserID: stream.OwnerUserID, TaskID: stream.ID,
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		Column5: cost, Model: usage.Model, UpdatedAt: timestamp(now),
	})
	if err != nil {
		return storeError("record task usage", err)
	}
	bucket := utcDate(stream.CreatedAt.Time)
	tasksRecorded := int64(0)
	if accumulated.Inserted {
		tasksRecorded = 1
	}
	if err := queries.UpsertAgentAppDailyUsage(ctx, agentdb.UpsertAgentAppDailyUsageParams{
		OwnerUserID: stream.OwnerUserID, AppInstanceID: appInstanceID,
		UtcDate: bucket, TasksRecorded: tasksRecorded,
		InputTokensRecorded: usage.InputTokens, OutputTokensRecorded: usage.OutputTokens,
		Column7: cost, CreatedAt: timestamp(now),
	}); err != nil {
		return storeError("record bucket usage", err)
	}
	if stream.BudgetMaxOutputTokens.Valid && stream.BudgetMaxOutputTokens.Int64 > 0 &&
		accumulated.OutputTokens > stream.BudgetMaxOutputTokens.Int64 {
		if err := queries.MarkAgentAppUsageBreach(ctx, agentdb.MarkAgentAppUsageBreachParams{
			UpdatedAt: timestamp(now), OwnerUserID: stream.OwnerUserID,
			AppInstanceID: appInstanceID, UtcDate: bucket,
		}); err != nil {
			return storeError("mark usage breach", err)
		}
		if err := queries.RequestTaskCancellation(ctx, agentdb.RequestTaskCancellationParams{
			UpdatedAt: timestamp(now), ID: stream.ID,
		}); err != nil {
			return storeError("request breach cancellation", err)
		}
		// Deterministic termination, in this same transaction: the task is
		// cancelled here, so a provider completion racing the breach is
		// refused by the terminal state instead of surviving until the
		// worker's next heartbeat observes the cancellation flag.
		payload, err := json.Marshal(map[string]any{
			"runCancelled": map[string]string{"reason": "app task exceeded its reserved output token budget"},
		})
		if err != nil {
			return fmt.Errorf("encode breach cancellation event: %w", err)
		}
		breached := domain.Task{ID: stream.ID, LastEventSequence: stream.LastEventSequence + 1}
		if err := advanceTaskWithSystemEvent(ctx, queries, &breached, domain.StateCancelled, "run_cancelled", payload, now); err != nil {
			return err
		}
		// The breached task can never be claimed; its pending request row is
		// finished so the outbox holds no unprocessed zombie.
		if err := queries.FinishPendingTaskRequest(ctx, agentdb.FinishPendingTaskRequestParams{
			ProcessedAt: timestamp(now), AggregateID: stream.ID,
		}); err != nil {
			return fmt.Errorf("finish breached task request: %w", err)
		}
		// Owner notification for the breach cancellation, a hard requirement
		// of this same transaction (ADR-0014); the receipt arbitrates against
		// any racing provider terminal event.
		breachFact, factErr := taskTerminalNotificationFact(stream.OwnerUserID, streamProjectIDString(stream.ProjectID), stream.ID, string(domain.StateCancelled))
		if factErr != nil {
			return factErr
		}
		if err := r.appendNotificationTx(ctx, tx, breachFact, now); err != nil {
			return err
		}
	}
	return nil
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
		return domain.Task{}, storeError("query agent task", err)
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

// utcDate normalizes a timestamp to its UTC calendar day for quota buckets.
func utcDate(value time.Time) pgtype.Date {
	day := value.UTC()
	return pgtype.Date{Time: time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
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
