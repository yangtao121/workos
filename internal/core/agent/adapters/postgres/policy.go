package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yangtao121/workos/internal/core/agent/adapters/postgres/agentdb"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/agent/ports"
)

// policyFromDB projects one stored policy row. Stored rows are written
// canonical; any drift is caller-visible corruption, not a silent default.
func policyFromDB(value agentdb.WorkosCoreAgentAppPolicy) domain.Policy {
	return domain.Policy{
		OwnerUserID:   value.OwnerUserID,
		AppInstanceID: value.AppInstanceID,
		ProjectID:     value.ProjectID,
		Spec: domain.PolicySpec{
			Mode:                             domain.PolicyMode(value.ExecutionMode),
			MaxOutputTokensPerTask:           value.MaxOutputTokensPerTask,
			MaxRuntimeSecondsPerTask:         value.MaxRuntimeSecondsPerTask,
			MaxTasksPerUTCDay:                value.MaxTasksPerUtcDay,
			MaxReservedOutputTokensPerUTCDay: value.MaxReservedOutputTokensPerUtcDay,
		},
		Source:   domain.PolicySourceExplicit,
		Revision: value.PolicyRevision,
	}
}

func (r *Repository) GetPolicy(ctx context.Context, ownerUserID, appInstanceID string) (domain.Policy, bool, error) {
	value, err := r.queries.GetAgentAppPolicy(ctx, agentdb.GetAgentAppPolicyParams{
		OwnerUserID: ownerUserID, AppInstanceID: appInstanceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Policy{}, false, nil
	}
	if err != nil {
		return domain.Policy{}, false, storeError("query app policy", err)
	}
	return policyFromDB(value), true, nil
}

// GetPolicyRequest reads one consumed SetAppPolicy key with its stored
// first-response snapshot.
func (r *Repository) GetPolicyRequest(ctx context.Context, ownerUserID, idempotencyKey string) (ports.PolicyRequestRecord, bool, error) {
	value, err := r.queries.GetAgentAppPolicyRequest(ctx, agentdb.GetAgentAppPolicyRequestParams{
		OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.PolicyRequestRecord{}, false, nil
	}
	if err != nil {
		return ports.PolicyRequestRecord{}, false, storeError("query app policy request", err)
	}
	return ports.PolicyRequestRecord{RequestDigest: value.RequestDigest, Result: value.Result}, true, nil
}

// SetPolicy adjudicates one full-replacement policy mutation in a single
// transaction: consumed-key replay/conflict first, then the optimistic
// expected-revision verdict against the locked row, then the policy upsert
// and — on a real change — the atomic invalidation of every pending approval
// of the same installation (their waiting tasks terminate with
// approval_expired events). The first response is snapshotted into the
// request row, so later policy changes never alter what a replay returns.
func (r *Repository) SetPolicy(ctx context.Context, command ports.SetPolicyCommand) (domain.Policy, ports.SetPolicyResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Policy{}, ports.SetPolicyResult{}, storeError("begin set app policy", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	existing, err := queries.GetAgentAppPolicyForUpdate(ctx, agentdb.GetAgentAppPolicyForUpdateParams{
		OwnerUserID: command.OwnerUserID, AppInstanceID: command.AppInstanceID,
	})
	// An absent policy row is the normal first-Set case; anything else that
	// failed to read is a real storage failure.
	existingFound := true
	if errors.Is(err, pgx.ErrNoRows) {
		existingFound = false
	} else if err != nil {
		return domain.Policy{}, ports.SetPolicyResult{}, storeError("lock app policy", err)
	}

	// The idempotency key is consumed even by a deterministic no-op, and a
	// failed mutation (stale revision, exhausted validation upstream) never
	// reaches this transaction.
	rows, err := queries.InsertAgentAppPolicyRequest(ctx, agentdb.InsertAgentAppPolicyRequestParams{
		OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
		RequestDigest: command.RequestDigest,
		Result:        []byte(`{"result_version":"1","phase":"pending"}`),
		CreatedAt:     timestamp(command.Now),
	})
	if err != nil {
		return domain.Policy{}, ports.SetPolicyResult{}, storeError("insert app policy request", err)
	}
	if rows == 0 {
		consumed, err := queries.GetAgentAppPolicyRequest(ctx, agentdb.GetAgentAppPolicyRequestParams{
			OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Policy{}, ports.SetPolicyResult{}, fmt.Errorf("policy request mapping vanished mid-transaction: %w", domain.ErrInvalid)
		}
		if err != nil {
			return domain.Policy{}, ports.SetPolicyResult{}, storeError("classify app policy request", err)
		}
		if consumed.RequestDigest != command.RequestDigest {
			return domain.Policy{}, ports.SetPolicyResult{}, domain.ErrIdempotencyConflict
		}
		policy, err := domain.DecodePolicyResult(consumed.Result)
		if err != nil {
			return domain.Policy{}, ports.SetPolicyResult{}, fmt.Errorf("stored policy result snapshot is invalid: %w", err)
		}
		policy.OwnerUserID = command.OwnerUserID
		policy.AppInstanceID = command.AppInstanceID
		if err := tx.Commit(ctx); err != nil {
			return domain.Policy{}, ports.SetPolicyResult{}, storeError("commit app policy replay", err)
		}
		return policy, ports.SetPolicyResult{Replay: true}, nil
	}

	if existingFound {
		if command.ExpectedPolicyRevision <= 0 || command.ExpectedPolicyRevision != existing.PolicyRevision {
			return domain.Policy{}, ports.SetPolicyResult{}, domain.ErrPolicyStale
		}
	} else if command.ExpectedPolicyRevision != 0 {
		return domain.Policy{}, ports.SetPolicyResult{}, domain.ErrPolicyStale
	}

	policy := domain.Policy{
		OwnerUserID: command.OwnerUserID, AppInstanceID: command.AppInstanceID,
		ProjectID: command.ProjectID, Spec: command.Spec,
		Source: domain.PolicySourceExplicit,
	}
	changed := true
	if existingFound && existing.SpecDigest == command.SpecDigest {
		// Deterministic no-op: identical spec, no revision bump, no approval
		// invalidation — but the key stays consumed and replays exactly.
		policy = policyFromDB(existing)
		changed = false
	} else {
		expected := int64(-1)
		if existingFound {
			expected = existing.PolicyRevision
		}
		if _, err := queries.UpsertAgentAppPolicy(ctx, agentdb.UpsertAgentAppPolicyParams{
			OwnerUserID: command.OwnerUserID, AppInstanceID: command.AppInstanceID,
			ProjectID: command.ProjectID, ExecutionMode: string(command.Spec.Mode),
			MaxOutputTokensPerTask:           command.Spec.MaxOutputTokensPerTask,
			MaxRuntimeSecondsPerTask:         command.Spec.MaxRuntimeSecondsPerTask,
			MaxTasksPerUtcDay:                command.Spec.MaxTasksPerUTCDay,
			MaxReservedOutputTokensPerUtcDay: command.Spec.MaxReservedOutputTokensPerUTCDay,
			SpecDigest:                       command.SpecDigest,
			CreatedAt:                        timestamp(command.Now),
			ExpectedRevision:                 expected,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// The conflict-path guard refused the update: either a stale
				// expected revision, or a concurrent first Set won the race
				// against our "no row exists" observation. Both are the same
				// fail-closed stale verdict.
				return domain.Policy{}, ports.SetPolicyResult{}, domain.ErrPolicyStale
			}
			return domain.Policy{}, ports.SetPolicyResult{}, storeError("upsert app policy", err)
		}
		policy.Revision = 1
		if existingFound {
			policy.Revision = expected + 1
		}

		// A real policy change atomically invalidates every pending approval
		// of this installation; their waiting tasks terminate with an
		// approval_expired event and never queue.
		expired, err := queries.ExpirePendingApprovals(ctx, agentdb.ExpirePendingApprovalsParams{
			UpdatedAt: timestamp(command.Now), OwnerUserID: command.OwnerUserID,
			AppInstanceID: command.AppInstanceID,
		})
		if err != nil {
			return domain.Policy{}, ports.SetPolicyResult{}, storeError("expire pending approvals", err)
		}
		for _, row := range expired {
			if err := r.expireApprovalTask(ctx, queries, command.OwnerUserID, row.TaskID, row.ID, command.Now); err != nil {
				return domain.Policy{}, ports.SetPolicyResult{}, err
			}
		}
	}
	policy.OwnerUserID = command.OwnerUserID
	policy.AppInstanceID = command.AppInstanceID
	snapshot, err := domain.EncodePolicyResult(policy)
	if err != nil {
		return domain.Policy{}, ports.SetPolicyResult{}, fmt.Errorf("encode policy result snapshot: %w", err)
	}
	if err := queries.UpdateAgentAppPolicyRequestResult(ctx, agentdb.UpdateAgentAppPolicyRequestResultParams{
		Result: snapshot, OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
	}); err != nil {
		return domain.Policy{}, ports.SetPolicyResult{}, storeError("store policy result snapshot", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Policy{}, ports.SetPolicyResult{}, storeError("commit app policy", err)
	}
	return policy, ports.SetPolicyResult{Changed: changed}, nil
}

// expireApprovalTask terminates one invalidated approval's waiting task with
// an approval_expired event. Already-terminal tasks are left untouched.
func (r *Repository) expireApprovalTask(ctx context.Context, queries *agentdb.Queries, ownerID, taskID, approvalID string, now time.Time) error {
	value, err := queries.GetAgentTaskForUpdate(ctx, agentdb.GetAgentTaskForUpdateParams{OwnerUserID: ownerID, ID: taskID})
	task, err := taskFromDB(value, err)
	if err != nil {
		return err
	}
	if task.State.Terminal() {
		return nil
	}
	task.State = domain.StateCancelled
	payload, err := json.Marshal(map[string]any{
		"approvalExpired": map[string]any{"approvalId": approvalID},
	})
	if err != nil {
		return fmt.Errorf("encode approval expired event: %w", err)
	}
	if err := advanceTaskWithSystemEvent(ctx, queries, &task, domain.StateCancelled, "approval_expired", payload, now); err != nil {
		return err
	}
	// Terminal event so every watch consumer sees the task end.
	cancelPayload, err := json.Marshal(map[string]any{
		"runCancelled": map[string]string{"reason": "app task approval expired"},
	})
	if err != nil {
		return fmt.Errorf("encode expiry terminal event: %w", err)
	}
	if err := advanceTaskWithSystemEvent(ctx, queries, &task, domain.StateCancelled, "run_cancelled", cancelPayload, now); err != nil {
		return err
	}
	// The cancelled task never queued through this approval; the outbox
	// finish is a defensive no-op for it.
	if err := queries.FinishPendingTaskRequest(ctx, agentdb.FinishPendingTaskRequestParams{
		ProcessedAt: timestamp(now), AggregateID: task.ID,
	}); err != nil {
		return fmt.Errorf("finish expired task request: %w", err)
	}
	return nil
}

func approvalFromDB(value agentdb.WorkosCoreAgentAppApproval) domain.Approval {
	approval := domain.Approval{
		OwnerUserID: value.OwnerUserID, ID: value.ID, AppInstanceID: value.AppInstanceID,
		ProjectID: value.ProjectID, TaskID: value.TaskID, AppID: value.AppID,
		GoalExcerpt: value.GoalExcerpt, ProviderID: value.ProviderID,
		Spec: domain.PolicySpec{
			MaxOutputTokensPerTask:           value.MaxOutputTokensPerTask,
			MaxRuntimeSecondsPerTask:         value.MaxRuntimeSecondsPerTask,
			MaxTasksPerUTCDay:                value.MaxTasksPerUtcDay,
			MaxReservedOutputTokensPerUTCDay: value.MaxReservedOutputTokensPerUtcDay,
		},
		Revision:  value.PolicyRevision,
		State:     domain.ApprovalState(value.State),
		CreatedAt: value.CreatedAt.Time,
		UpdatedAt: value.UpdatedAt.Time,
	}
	if value.DecidedIdempotencyKey.Valid {
		approval.DecidedIdempotencyKey = value.DecidedIdempotencyKey.String
	}
	if value.DecisionDigest.Valid {
		approval.DecisionDigest = value.DecisionDigest.String
	}
	if value.DecidedAt.Valid {
		approval.DecidedAt = value.DecidedAt.Time
	}
	return approval
}

func (r *Repository) GetApproval(ctx context.Context, ownerUserID, approvalID string) (domain.Approval, error) {
	value, err := r.queries.GetAgentAppApproval(ctx, agentdb.GetAgentAppApprovalParams{
		OwnerUserID: ownerUserID, ID: approvalID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Approval{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, storeError("query app approval", err)
	}
	return approvalFromDB(value), nil
}

func (r *Repository) ListApprovals(ctx context.Context, ownerUserID, projectID string, state domain.ApprovalState, cursor string, limit int) ([]domain.Approval, error) {
	if cursor == "" {
		cursor = "00000000-0000-0000-0000-000000000000"
	}
	values, err := r.queries.ListAgentAppApprovals(ctx, agentdb.ListAgentAppApprovalsParams{
		OwnerUserID: ownerUserID, Cursor: cursor, ProjectID: projectID, State: string(state),
		RowLimit: int32(limit),
	})
	if err != nil {
		return nil, storeError("list app approvals", err)
	}
	result := make([]domain.Approval, 0, len(values))
	for _, value := range values {
		result = append(result, approvalFromDB(value))
	}
	return result, nil
}

// DecideApproval adjudicates one owner decision inside a single transaction:
// decision idempotency replay/conflict, guarded daily reservation (approve),
// waiting→queued + approval_decided event + claimable outbox (approve), or
// terminal rejection with no outbox and no reservation (reject). Any failure
// leaves the approval exactly as it was — pending and quota-free.
func (r *Repository) DecideApproval(ctx context.Context, command ports.DecideApprovalCommand) (domain.Approval, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Approval{}, storeError("begin approval decision", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	value, err := queries.GetAgentAppApprovalForUpdate(ctx, agentdb.GetAgentAppApprovalForUpdateParams{
		OwnerUserID: command.OwnerUserID, ID: command.ApprovalID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Approval{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, storeError("lock app approval", err)
	}
	approval := approvalFromDB(value)

	switch approval.State {
	case domain.ApprovalExpired:
		return domain.Approval{}, domain.ErrApprovalNotPending
	case domain.ApprovalApproved, domain.ApprovalRejected:
		// Terminal: the decision key either replays the exact first decision
		// or conflicts — a second, different decision never re-runs.
		if approval.DecidedIdempotencyKey != command.IdempotencyKey {
			return domain.Approval{}, domain.ErrApprovalAlreadyDecided
		}
		if approval.DecisionDigest != command.DecisionDigest {
			return domain.Approval{}, domain.ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Approval{}, storeError("commit approval replay", err)
		}
		return approval, nil
	case domain.ApprovalPending:
	default:
		return domain.Approval{}, fmt.Errorf("approval state %q is invalid: %w", approval.State, domain.ErrInvalid)
	}

	taskValue, err := queries.GetAgentTaskForUpdate(ctx, agentdb.GetAgentTaskForUpdateParams{
		OwnerUserID: command.OwnerUserID, ID: approval.TaskID,
	})
	task, err := taskFromDB(taskValue, err)
	if err != nil {
		return domain.Approval{}, err
	}
	if task.State != domain.StateWaiting {
		return domain.Approval{}, domain.ErrApprovalNotPending
	}

	decidedEventPayload := func(decision string) (json.RawMessage, error) {
		return json.Marshal(map[string]any{
			"approvalDecided": map[string]any{
				"approvalId": approval.ID,
				"decision":   decision,
			},
		})
	}

	if command.Decision == domain.ApprovalDecisionApprove {
		// Guarded reservation against the approval's own policy-spec
		// snapshot: crossing either daily ceiling refuses the increment and
		// the whole decision rolls back, so concurrent decisions can never
		// oversell the bucket.
		if _, err := queries.ReserveAgentAppDailyQuota(ctx, agentdb.ReserveAgentAppDailyQuotaParams{
			OwnerUserID: approval.OwnerUserID, AppInstanceID: approval.AppInstanceID,
			UtcDate:              utcDate(command.Now),
			OutputTokensReserved: approval.Spec.MaxOutputTokensPerTask,
			PolicyRevision:       approval.Revision,
			CreatedAt:            timestamp(command.Now),
			MaxTasks:             approval.Spec.MaxTasksPerUTCDay,
			MaxReservedTokens:    approval.Spec.MaxReservedOutputTokensPerUTCDay,
		}); errors.Is(err, pgx.ErrNoRows) {
			if err := classifyQuotaFailure(ctx, queries, approval.OwnerUserID, approval.AppInstanceID, command.Now); err != nil {
				return domain.Approval{}, err
			}
			return domain.Approval{}, domain.ErrQuotaExhausted
		} else if err != nil {
			return domain.Approval{}, storeError("reserve approval quota", err)
		}
		payload, err := decidedEventPayload("APP_AGENT_APPROVAL_DECISION_APPROVE")
		if err != nil {
			return domain.Approval{}, fmt.Errorf("encode approval decided event: %w", err)
		}
		task.State = domain.StateQueued
		if err := advanceTaskWithSystemEvent(ctx, queries, &task, domain.StateQueued, "approval_decided", payload, command.Now); err != nil {
			return domain.Approval{}, err
		}
		outboxPayload, err := json.Marshal(map[string]string{"taskId": task.ID})
		if err != nil {
			return domain.Approval{}, fmt.Errorf("encode task request: %w", err)
		}
		outboxID, err := uuid.NewV7()
		if err != nil {
			return domain.Approval{}, fmt.Errorf("generate task outbox id: %w", err)
		}
		if err := queries.InsertTaskOutbox(ctx, agentdb.InsertTaskOutboxParams{
			ID: outboxID.String(), AggregateID: task.ID, Payload: outboxPayload, OccurredAt: timestamp(command.Now),
		}); err != nil {
			return domain.Approval{}, storeError("append task outbox", err)
		}
	} else {
		if err := queries.FinishPendingTaskRequest(ctx, agentdb.FinishPendingTaskRequestParams{
			ProcessedAt: timestamp(command.Now), AggregateID: task.ID,
		}); err != nil {
			return domain.Approval{}, fmt.Errorf("finish rejected task request: %w", err)
		}
		payload, err := decidedEventPayload("APP_AGENT_APPROVAL_DECISION_REJECT")
		if err != nil {
			return domain.Approval{}, fmt.Errorf("encode approval decided event: %w", err)
		}
		task.State = domain.StateCancelled
		if err := advanceTaskWithSystemEvent(ctx, queries, &task, domain.StateCancelled, "approval_decided", payload, command.Now); err != nil {
			return domain.Approval{}, err
		}
		// A terminal event ends every watch stream exactly like an owner
		// cancellation would; the decision event above carries the why.
		cancelPayload, err := json.Marshal(map[string]any{
			"runCancelled": map[string]string{"reason": "app task approval rejected"},
		})
		if err != nil {
			return domain.Approval{}, fmt.Errorf("encode rejection terminal event: %w", err)
		}
		if err := advanceTaskWithSystemEvent(ctx, queries, &task, domain.StateCancelled, "run_cancelled", cancelPayload, command.Now); err != nil {
			return domain.Approval{}, err
		}
	}
	state := "approved"
	if command.Decision == domain.ApprovalDecisionReject {
		state = "rejected"
	}
	if rows, err := queries.DecideAgentAppApproval(ctx, agentdb.DecideAgentAppApprovalParams{
		State: state, IdempotencyKey: text(command.IdempotencyKey), DecisionDigest: text(command.DecisionDigest),
		DecidedAt: timestamp(command.Now), OwnerUserID: command.OwnerUserID, ApprovalID: command.ApprovalID,
	}); err != nil {
		return domain.Approval{}, storeError("decide app approval", err)
	} else if rows == 0 {
		return domain.Approval{}, domain.ErrApprovalNotPending
	}
	approval.State = domain.ApprovalState(state)
	approval.DecidedIdempotencyKey = command.IdempotencyKey
	approval.DecisionDigest = command.DecisionDigest
	approval.DecidedAt = command.Now
	approval.UpdatedAt = command.Now
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, storeError("commit approval decision", err)
	}
	return approval, nil
}

// classifyQuotaFailure distinguishes a circuit-broken bucket from an honestly
// exhausted one. Both fail closed with ResourceExhausted semantics; the
// distinction is audit-visible, never a user-facing sentence difference.
func classifyQuotaFailure(ctx context.Context, queries *agentdb.Queries, ownerUserID, appInstanceID string, now time.Time) error {
	bucket := utcDate(now)
	usage, err := queries.GetAgentAppDailyUsage(ctx, agentdb.GetAgentAppDailyUsageParams{
		OwnerUserID: ownerUserID, AppInstanceID: appInstanceID, UtcDate: bucket,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return storeError("classify quota failure", err)
	}
	if usage.QuotaBreached {
		return domain.ErrQuotaBreached
	}
	return nil
}

// GetAppDailyUsage reads the reservation bucket and the usage projection for
// one (installation, UTC date); missing rows read as zeroes.
func (r *Repository) GetAppDailyUsage(ctx context.Context, ownerUserID, appInstanceID, date string) (ports.DailyUsage, error) {
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ports.DailyUsage{}, domain.ErrInvalid
	}
	bucket := utcDate(day)
	result := ports.DailyUsage{UTCDate: date}
	reservation, err := r.queries.GetAgentAppDailyReservations(ctx, agentdb.GetAgentAppDailyReservationsParams{
		OwnerUserID: ownerUserID, AppInstanceID: appInstanceID, UtcDate: bucket,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return ports.DailyUsage{}, storeError("query app daily reservations", err)
	default:
		result.TasksReserved = reservation.TasksReserved
		result.OutputTokensReserved = reservation.OutputTokensReserved
		result.ReservationRevision = reservation.PolicyRevision
	}
	usage, err := r.queries.GetAgentAppDailyUsage(ctx, agentdb.GetAgentAppDailyUsageParams{
		OwnerUserID: ownerUserID, AppInstanceID: appInstanceID, UtcDate: bucket,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return ports.DailyUsage{}, storeError("query app daily usage", err)
	default:
		result.TasksRecorded = usage.TasksRecorded
		result.InputTokensRecorded = usage.InputTokensRecorded
		result.OutputTokensRecorded = usage.OutputTokensRecorded
		result.QuotaBreached = usage.QuotaBreached
		if usage.CostDecimalRecorded.Valid {
			if value, err := usage.CostDecimalRecorded.Value(); err == nil {
				if text, ok := value.(string); ok {
					result.CostDecimalRecorded = text
					result.CostAvailable = true
				}
			}
		}
	}
	return result, nil
}
