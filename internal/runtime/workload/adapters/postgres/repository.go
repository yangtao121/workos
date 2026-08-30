// Package postgres adapts the Workload Manager's repository port to the
// runtime-owned workloads tables. Transient driver failures wrap the
// ErrStoreUnavailable sentinel at the port boundary; classification never
// reads SQLSTATE message text or constraint names.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/runtime/workload/adapters/postgres/workloaddb"
	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

// Repository is the pgx-backed WorkloadRepository.
type Repository struct {
	pool    *pgxpool.Pool
	queries workloaddb.Querier
}

func New(pool *pgxpool.Pool) (*Repository, error) {
	if pool == nil {
		return nil, errors.New("workload repository requires a connection pool")
	}
	return &Repository{pool: pool, queries: workloaddb.New(pool)}, nil
}

// ReserveEnsure inserts the workload row and the ensure operation in one
// transaction. Any conflict (active slot or operation key) rolls back and
// reports reserved=false so the caller re-reads and classifies.
func (r *Repository) ReserveEnsure(ctx context.Context, workload domain.Workload, op domain.WorkloadOperation) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, storeError("reserve workload", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := workloaddb.New(tx)
	if err := queries.InsertWorkload(ctx, workloadParams(workload)); err != nil {
		if dbtransient.IsTransient(err) {
			return false, storeError("reserve workload", err)
		}
		if isUniqueViolation(err) {
			// The workload ID or active owner+instance slot was won by a
			// concurrent reserve; the application re-reads authoritative facts.
			return false, nil
		}
		// CHECK/FK/programming failures are invariant errors, not conflicts.
		return false, storeError("reserve workload", err)
	}
	rows, err := queries.InsertWorkloadOperation(ctx, workloaddb.InsertWorkloadOperationParams{
		WorkloadID:       op.WorkloadID,
		OperationKey:     op.OperationKey,
		Operation:        string(op.Operation),
		RequestDigest:    op.RequestDigest,
		ResultGeneration: int8Param(op.ResultGeneration, op.ResultGeneration > 0),
		CreatedAt:        op.CreatedAt,
		UpdatedAt:        op.UpdatedAt,
	})
	if err != nil {
		if dbtransient.IsTransient(err) {
			return false, storeError("reserve workload operation", err)
		}
		return false, storeError("reserve workload operation", err)
	}
	if rows == 0 {
		// Same key reserved earlier: not a fresh reserve.
		return false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, storeError("commit workload reserve", err)
	}
	return true, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (r *Repository) Get(ctx context.Context, workloadID string) (domain.Workload, error) {
	if !domain.ValidWorkloadID(workloadID) {
		return domain.Workload{}, domain.ErrInvalid
	}
	row, err := r.queries.GetWorkload(ctx, workloadID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Workload{}, domain.ErrNotFound
		}
		return domain.Workload{}, storeError("get workload", err)
	}
	return workloadFromRow(row), nil
}

func (r *Repository) GetActiveByInstance(ctx context.Context, ownerUserID, appInstanceID string) (domain.Workload, error) {
	row, err := r.queries.GetActiveWorkloadByInstance(ctx, workloaddb.GetActiveWorkloadByInstanceParams{
		OwnerUserID:   ownerUserID,
		AppInstanceID: appInstanceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Workload{}, domain.ErrNotFound
		}
		return domain.Workload{}, storeError("get active workload", err)
	}
	return workloadFromRow(row), nil
}

func (r *Repository) List(ctx context.Context, limit int) ([]domain.Workload, error) {
	rows, err := r.queries.ListWorkloads(ctx, int32(limit))
	if err != nil {
		return nil, storeError("list workloads", err)
	}
	workloads := make([]domain.Workload, 0, len(rows))
	for _, row := range rows {
		workloads = append(workloads, workloadFromRow(row))
	}
	return workloads, nil
}

func (r *Repository) LookupOperation(ctx context.Context, workloadID, operationKey string) (ports.StoredOperation, error) {
	row, err := r.queries.GetWorkloadOperation(ctx, workloaddb.GetWorkloadOperationParams{
		WorkloadID:   workloadID,
		OperationKey: operationKey,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.StoredOperation{}, nil
		}
		return ports.StoredOperation{}, storeError("lookup workload operation", err)
	}
	stored := ports.StoredOperation{
		WorkloadID: row.WorkloadID, OperationKey: row.OperationKey,
		Operation: domain.Operation(row.Operation), RequestDigest: row.RequestDigest,
	}
	if row.ResultState.Valid {
		stored.ResultState = domain.State(row.ResultState.String)
	}
	if row.ResultGeneration.Valid {
		stored.ResultGeneration = row.ResultGeneration.Int64
	}
	if row.ErrorKind.Valid {
		stored.ErrorKind = domain.ErrorKind(row.ErrorKind.String)
	}
	return stored, nil
}

func (r *Repository) PendingOperation(ctx context.Context, workloadID string, generation int64) (ports.StoredOperation, error) {
	row, err := r.queries.GetPendingWorkloadOperation(ctx, workloaddb.GetPendingWorkloadOperationParams{
		WorkloadID: workloadID, Generation: int8Param(generation, generation > 0),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.StoredOperation{}, nil
		}
		return ports.StoredOperation{}, storeError("get pending workload operation", err)
	}
	stored := ports.StoredOperation{
		WorkloadID: row.WorkloadID, OperationKey: row.OperationKey,
		Operation: domain.Operation(row.Operation), RequestDigest: row.RequestDigest,
	}
	if row.ResultState.Valid {
		stored.ResultState = domain.State(row.ResultState.String)
	}
	if row.ResultGeneration.Valid {
		stored.ResultGeneration = row.ResultGeneration.Int64
	}
	if row.ErrorKind.Valid {
		stored.ErrorKind = domain.ErrorKind(row.ErrorKind.String)
	}
	return stored, nil
}

func (r *Repository) RecordOperation(ctx context.Context, op domain.WorkloadOperation) error {
	return r.recordOperation(ctx, r.queries, op)
}

func (r *Repository) recordOperation(ctx context.Context, queries workloaddb.Querier, op domain.WorkloadOperation) error {
	if op.CreatedAt.IsZero() {
		op.CreatedAt = op.UpdatedAt
	}
	rows, err := queries.UpsertWorkloadOperation(ctx, workloaddb.UpsertWorkloadOperationParams{
		WorkloadID:       op.WorkloadID,
		OperationKey:     op.OperationKey,
		Operation:        string(op.Operation),
		RequestDigest:    op.RequestDigest,
		ResultState:      textParam(string(op.ResultState)),
		ResultGeneration: int8Param(op.ResultGeneration, op.ResultGeneration > 0),
		ErrorKind:        textParam(string(op.ErrorKind)),
		CreatedAt:        op.CreatedAt,
		UpdatedAt:        op.UpdatedAt,
	})
	if err != nil {
		return storeError("record workload operation", err)
	}
	if rows == 0 {
		stored, lookupErr := queries.GetWorkloadOperation(ctx, workloaddb.GetWorkloadOperationParams{
			WorkloadID: op.WorkloadID, OperationKey: op.OperationKey,
		})
		if lookupErr != nil {
			return storeError("read immutable workload operation", lookupErr)
		}
		resultState := ""
		if stored.ResultState.Valid {
			resultState = stored.ResultState.String
		}
		resultGeneration := int64(0)
		if stored.ResultGeneration.Valid {
			resultGeneration = stored.ResultGeneration.Int64
		}
		errorKind := ""
		if stored.ErrorKind.Valid {
			errorKind = stored.ErrorKind.String
		}
		if stored.Operation == string(op.Operation) && stored.RequestDigest == op.RequestDigest &&
			resultState == string(op.ResultState) && resultGeneration == op.ResultGeneration &&
			errorKind == string(op.ErrorKind) {
			return nil
		}
		return domain.ErrIdempotencyConflict
	}
	return nil
}

// Transition applies the guarded state transitions. Terminal rows never move.
func (r *Repository) Transition(ctx context.Context, workloadID string, from, to domain.State, facts ports.WorkloadFacts, now time.Time) error {
	return r.transition(ctx, r.queries, workloadID, from, to, facts, now)
}

func (r *Repository) transition(ctx context.Context, queries workloaddb.Querier, workloadID string, from, to domain.State, facts ports.WorkloadFacts, now time.Time) error {
	if facts.VerifiedAt != nil && from == to {
		// Verification stamping is bookkeeping on an unchanged state: the
		// dedicated guarded UPDATE keeps every other column (container
		// facts included) untouched.
		rows, err := queries.StampWorkloadVerified(ctx, workloaddb.StampWorkloadVerifiedParams{
			VerifiedAt: facts.VerifiedAt,
			Generation: facts.Generation,
			ID:         workloadID,
		})
		if err != nil {
			return storeError("stamp workload verified", err)
		}
		if rows == 0 {
			return domain.ErrNotFound
		}
		return nil
	}
	switch {
	case to == domain.StateRunning:
		rows, err := queries.SetWorkloadRunning(ctx, workloaddb.SetWorkloadRunningParams{
			ContainerID:             textParam(facts.ContainerID),
			Endpoint:                textParam(facts.Endpoint),
			CgroupPath:              textParam(facts.CgroupPath),
			HealthVerdict:           facts.HealthVerdict,
			LastExitCategory:        facts.LastExit,
			BaselineMemoryEventsOom: int64(facts.BaselineOOM),
			BaselinePidsEventsPeak:  int64(facts.BaselinePids),
			StartedAt:               timeParam(facts.StartedAt),
			UpdatedAt:               now,
			ID:                      workloadID,
			Generation:              facts.Generation,
		})
		if err != nil {
			return storeError("transition workload running", err)
		}
		if rows == 0 {
			return domain.ErrNotFound
		}
		return nil
	case to == domain.StateStarting && from != domain.StatePending:
		rows, err := queries.RestartWorkloadFrom(ctx, workloaddb.RestartWorkloadFromParams{
			FromState:    string(from),
			Generation:   facts.Generation,
			RestartCount: int32(facts.RestartCount),
			UpdatedAt:    now,
			ID:           workloadID,
		})
		if err != nil {
			return storeError("transition workload starting", err)
		}
		if rows == 0 {
			return domain.ErrNotFound
		}
		return nil
	default:
		rows, err := queries.SetWorkloadState(ctx, workloaddb.SetWorkloadStateParams{
			ToState:          string(to),
			ContainerID:      textParam(facts.ContainerID),
			Endpoint:         textParam(facts.Endpoint),
			CgroupPath:       textParam(facts.CgroupPath),
			HealthVerdict:    facts.HealthVerdict,
			LastExitCategory: facts.LastExit,
			StoppedAt:        timeParam(facts.StoppedAt),
			UpdatedAt:        now,
			ID:               workloadID,
			FromState:        string(from),
			Generation:       facts.Generation,
		})
		if err != nil {
			return storeError("transition workload state", err)
		}
		if rows == 0 {
			return domain.ErrNotFound
		}
		return nil
	}
}

func (r *Repository) TransitionOperation(ctx context.Context, workloadID string, from, to domain.State, facts ports.WorkloadFacts, op domain.WorkloadOperation, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return storeError("begin workload transition operation", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := workloaddb.New(tx)
	if err := r.transition(ctx, queries, workloadID, from, to, facts, now); err != nil {
		return err
	}
	if err := r.recordOperation(ctx, queries, op); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return storeError("commit workload transition operation", err)
	}
	return nil
}

func (r *Repository) ClaimLease(ctx context.Context, workloadID, owner string, until time.Time) (bool, error) {
	now := time.Now().UTC()
	rows, err := r.queries.ClaimWorkloadLease(ctx, workloaddb.ClaimWorkloadLeaseParams{
		LeaseOwner:     textParam(owner),
		LeaseExpiresAt: &until,
		ID:             workloadID,
		Now:            &now,
	})
	if err != nil {
		return false, storeError("claim workload lease", err)
	}
	return rows > 0, nil
}

func (r *Repository) SetIdle(ctx context.Context, workloadID string, generation int64, idle bool, now time.Time) (*time.Time, error) {
	if idle {
		stamp := now
		idleSince, err := r.queries.MarkWorkloadIdle(ctx, workloaddb.MarkWorkloadIdleParams{
			IdleSince: &stamp, ID: workloadID, Generation: generation,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, domain.ErrNotFound
			}
			return nil, storeError("mark workload idle", err)
		}
		return idleSince, nil
	}
	rows, err := r.queries.ClearWorkloadIdle(ctx, workloaddb.ClearWorkloadIdleParams{
		ID: workloadID, Generation: generation,
	})
	if err != nil {
		return nil, storeError("clear workload idle", err)
	}
	if rows == 0 {
		// Clearing an already-clear value is converged only if this exact
		// running generation still exists. Re-read so a stale reconciler cannot
		// silently claim success against a newer generation.
		workload, getErr := r.Get(ctx, workloadID)
		if getErr != nil {
			return nil, getErr
		}
		if workload.State != domain.StateRunning || workload.Generation != generation {
			return nil, domain.ErrNotFound
		}
	}
	return nil, nil
}

func workloadFromRow(row workloaddb.WorkosRuntimeWorkload) domain.Workload {
	var command []string
	_ = json.Unmarshal(row.Command, &command)
	var requested domain.RequestedPolicy
	_ = json.Unmarshal(row.RequestedPolicy, &requested)
	workload := domain.Workload{
		ID: row.ID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
		AppInstanceID: row.AppInstanceID, AppID: row.AppID, AppVersion: row.AppVersion,
		ManifestDigest: row.ManifestDigest, Image: row.Image, Command: command,
		Port: int64(row.Port), Requested: requested,
		Effective: domain.EffectivePolicy{
			CPUQuotaUSec:    row.EffectiveCpuQuotaUs,
			MemoryHighBytes: row.EffectiveMemoryHighBytes,
			MemoryMaxBytes:  row.EffectiveMemoryMaxBytes,
			PidsMax:         int64(row.EffectivePidsMax),
			StartupTimeout:  time.Duration(row.EffectiveStartupSeconds) * time.Second,
			RestartLimit:    int64(row.EffectiveRestartLimit),
			HealthPath:      requested.HTTPPath,
		},
		Generation:    row.Generation,
		State:         domain.State(row.State),
		RestartCount:  int64(row.RestartCount),
		ContainerName: row.ContainerName,
		HealthVerdict: row.HealthVerdict,
		LastExit:      row.LastExitCategory,
		BaselineOOM:   uint64(row.BaselineMemoryEventsOom),
		BaselinePids:  uint64(row.BaselinePidsEventsPeak),
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
	if row.IdleSince != nil {
		workload.IdleSince = row.IdleSince
	}
	if row.ContainerID.Valid {
		workload.ContainerID = row.ContainerID.String
	}
	if row.Endpoint.Valid {
		workload.Endpoint = row.Endpoint.String
	}
	if row.CgroupPath.Valid {
		workload.CgroupPath = row.CgroupPath.String
	}
	if row.LastVerifiedAt != nil {
		workload.LastVerifiedAt = row.LastVerifiedAt
	}
	if row.LeaseExpiresAt != nil {
		workload.LeaseExpiresAt = row.LeaseExpiresAt
	}
	if row.StartedAt != nil {
		workload.StartedAt = row.StartedAt
	}
	if row.StoppedAt != nil {
		workload.StoppedAt = row.StoppedAt
	}
	return workload
}

func workloadParams(workload domain.Workload) workloaddb.InsertWorkloadParams {
	command, err := json.Marshal(workload.Command)
	if err != nil {
		command = []byte("[]")
	}
	requested, err := json.Marshal(workload.Requested)
	if err != nil {
		requested = []byte("{}")
	}
	return workloaddb.InsertWorkloadParams{
		ID: workload.ID, OwnerUserID: workload.OwnerUserID, ProjectID: workload.ProjectID,
		AppInstanceID: workload.AppInstanceID, AppID: workload.AppID, AppVersion: workload.AppVersion,
		ManifestDigest: workload.ManifestDigest, Image: workload.Image,
		Command: command, Port: int32(workload.Port),
		RequestedPolicy: requested, PolicyVersion: domain.PolicyVersion,
		EffectiveCpuQuotaUs:      workload.Effective.CPUQuotaUSec,
		EffectiveMemoryHighBytes: workload.Effective.MemoryHighBytes,
		EffectiveMemoryMaxBytes:  workload.Effective.MemoryMaxBytes,
		EffectivePidsMax:         int32(workload.Effective.PidsMax),
		EffectiveStartupSeconds:  int32(workload.Effective.StartupTimeout / time.Second),
		EffectiveRestartLimit:    int32(workload.Effective.RestartLimit),
		Generation:               workload.Generation,
		State:                    string(workload.State),
		ContainerName:            workload.ContainerName,
		HealthVerdict:            workload.HealthVerdict,
		LastExitCategory:         workload.LastExit,
		CreatedAt:                workload.CreatedAt,
		UpdatedAt:                workload.UpdatedAt,
	}
}

func textParam(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func int8Param(value int64, valid bool) pgtype.Int8 {
	return pgtype.Int8{Int64: value, Valid: valid}
}

func timeParam(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func storeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", operation, ports.ErrStoreUnavailable, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
