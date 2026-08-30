// Package postgres adapts the Reliability module's repository port to the
// reliability-owned tables. Transient driver failures wrap the sanitized
// domain.ErrUnavailable verdict at the port boundary; classification never
// reads SQLSTATE message text or constraint names.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/reliability/adapters/postgres/reliabilitydb"
	"github.com/yangtao121/workos/internal/reliability/domain"
	"github.com/yangtao121/workos/internal/reliability/ports"
)

// Repository is the pgx-backed IncidentRepository.
type Repository struct {
	pool    *pgxpool.Pool
	queries *reliabilitydb.Queries
}

func New(pool *pgxpool.Pool) (*Repository, error) {
	if pool == nil {
		return nil, errors.New("reliability repository requires a connection pool")
	}
	return &Repository{pool: pool, queries: reliabilitydb.New(pool)}, nil
}

// CreateIncident inserts the incident; an existing occurrence digest keeps
// the stored episode authoritative and reports created=false.
func (r *Repository) CreateIncident(ctx context.Context, incident domain.Incident) (bool, error) {
	rows, err := r.queries.InsertIncident(ctx, reliabilitydb.InsertIncidentParams{
		ID: incident.ID, OwnerUserID: incident.OwnerUserID, ProjectID: incident.ProjectID,
		AppInstanceID: incident.AppInstanceID, AppID: incident.AppID,
		WorkloadID: incident.WorkloadID, WorkloadGeneration: incident.WorkloadGeneration,
		Violation: string(incident.Violation), Severity: string(incident.Violation.Severity()),
		Summary: incident.Summary, OccurrenceDigest: incident.OccurrenceDigest,
		EvidenceDigest: incident.EvidenceDigest, State: string(incident.State),
		RestartOutcome: string(incident.RestartOutcome), Revision: incident.Revision,
		CreatedAt: incident.CreatedAt, UpdatedAt: incident.UpdatedAt,
	})
	if err != nil {
		return false, storeError("create incident", err)
	}
	return rows > 0, nil
}

func (r *Repository) GetIncident(ctx context.Context, incidentID string) (domain.Incident, error) {
	if !domain.ValidUUIDv7(incidentID) {
		return domain.Incident{}, domain.ErrInvalid
	}
	row, err := r.queries.GetIncident(ctx, incidentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Incident{}, domain.ErrNotFound
		}
		return domain.Incident{}, storeError("get incident", err)
	}
	return incidentFromRow(row), nil
}

func (r *Repository) GetIncidentByOccurrence(ctx context.Context, occurrenceDigest string) (domain.Incident, error) {
	row, err := r.queries.GetIncidentByOccurrence(ctx, occurrenceDigest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Incident{}, domain.ErrNotFound
		}
		return domain.Incident{}, storeError("get incident by occurrence", err)
	}
	return incidentFromRow(row), nil
}

func (r *Repository) ListIncidents(ctx context.Context, filter ports.IncidentFilter, limit int) ([]domain.Incident, error) {
	rows, err := r.queries.ListIncidentsPage(ctx, reliabilitydb.ListIncidentsPageParams{
		OwnerUserID: filter.OwnerUserID, ProjectID: filter.ProjectID,
		PageToken: filter.PageToken, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, storeError("list incidents", err)
	}
	incidents := make([]domain.Incident, 0, len(rows))
	for _, row := range rows {
		incidents = append(incidents, incidentFromRow(row))
	}
	return incidents, nil
}

func (r *Repository) UpdateOutcome(ctx context.Context, incidentID string, state domain.State, outcome domain.RestartOutcome, now time.Time) error {
	var mitigatedAt *time.Time
	if state == domain.StateMitigated {
		stamped := now
		mitigatedAt = &stamped
	}
	rows, err := r.queries.UpdateIncidentOutcome(ctx, reliabilitydb.UpdateIncidentOutcomeParams{
		State: string(state), RestartOutcome: string(outcome),
		MitigatedAt: mitigatedAt, UpdatedAt: now, ID: incidentID,
	})
	if err != nil {
		return storeError("update incident outcome", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *Repository) MarkResolved(ctx context.Context, incidentID string, now time.Time) error {
	rows, err := r.queries.MarkIncidentResolved(ctx, reliabilitydb.MarkIncidentResolvedParams{
		ResolvedAt: &now, UpdatedAt: now, ID: incidentID,
	})
	if err != nil {
		return storeError("resolve incident", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Acknowledge stamps the owner acknowledgement with its durable idempotency
// key. Same key replays the same acknowledged state; a key already used on a
// different incident of the same owner is a stable conflict (checked before
// the write and enforced by the 017 partial unique index under concurrency).
func (r *Repository) Acknowledge(ctx context.Context, incidentID, ownerUserID, acknowledgeKey string, now time.Time) error {
	if conflict, err := r.queries.IncidentAcknowledgeKeyExists(ctx, reliabilitydb.IncidentAcknowledgeKeyExistsParams{
		OwnerUserID: ownerUserID, AcknowledgeKey: textParam(acknowledgeKey), ID: incidentID,
	}); err != nil {
		return storeError("check acknowledge key", err)
	} else if conflict {
		return domain.ErrIdempotencyConflict
	}
	rows, err := r.queries.AcknowledgeIncident(ctx, reliabilitydb.AcknowledgeIncidentParams{
		AcknowledgedAt: &now, AcknowledgeKey: textParam(acknowledgeKey),
		UpdatedAt: now, ID: incidentID, OwnerUserID: ownerUserID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrIdempotencyConflict
		}
		return storeError("acknowledge incident", err)
	}
	if rows == 0 {
		// Already acknowledged (no-op success) or foreign/unknown. The
		// re-read classifies without leaking which.
		if _, getErr := r.GetIncident(ctx, incidentID); getErr != nil {
			return getErr
		}
	}
	return nil
}

// isUniqueViolation classifies the SQLSTATE unique-violation code for the
// acknowledge-key conflict; the code is a classification input only and
// never reaches an error message or log.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

func (r *Repository) ListOpenForWorkload(ctx context.Context, workloadID string, generation int64) ([]domain.Incident, error) {
	rows, err := r.queries.ListOpenIncidentsForWorkload(ctx, reliabilitydb.ListOpenIncidentsForWorkloadParams{
		WorkloadID: workloadID, WorkloadGeneration: generation,
	})
	if err != nil {
		return nil, storeError("list open incidents", err)
	}
	incidents := make([]domain.Incident, 0, len(rows))
	for _, row := range rows {
		incidents = append(incidents, incidentFromRow(row))
	}
	return incidents, nil
}

// RecordAction stores or updates the action ledger row. Empty outcomes
// reserve the row so a crash before the control call still replays the same
// action key.
func (r *Repository) RecordAction(ctx context.Context, incidentID, action string, result ports.ControlResult, now time.Time) error {
	if result.Outcome == "" {
		result.Outcome = ports.ControlUnavailable
	}
	err := r.queries.UpsertIncidentAction(ctx, reliabilitydb.UpsertIncidentActionParams{
		IncidentID: incidentID, Action: action,
		ActionKey: actionKey(incidentID, action), Outcome: string(result.Outcome),
		ResultGeneration: pgtype.Int8{Int64: result.Generation, Valid: result.Generation > 0},
		CreatedAt:        now, UpdatedAt: now,
	})
	if err != nil {
		return storeError("record incident action", err)
	}
	return nil
}

func (r *Repository) LookupAction(ctx context.Context, incidentID, action string) (ports.StoredAction, error) {
	row, err := r.queries.GetIncidentAction(ctx, reliabilitydb.GetIncidentActionParams{
		IncidentID: incidentID, Action: action,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.StoredAction{}, nil
		}
		return ports.StoredAction{}, storeError("lookup incident action", err)
	}
	return ports.StoredAction{
		IncidentID: row.IncidentID, Action: row.Action,
		Outcome:          ports.ControlOutcome(row.Outcome),
		ResultGeneration: row.ResultGeneration.Int64,
	}, nil
}

// ListPendingActionIncidents returns open incidents for decision re-driving.
func (r *Repository) ListPendingActionIncidents(ctx context.Context, limit int) ([]domain.Incident, error) {
	rows, err := r.queries.ListIncidentsPage(ctx, reliabilitydb.ListIncidentsPageParams{
		OwnerUserID: "%", ProjectID: "", PageToken: "", RowLimit: int32(limit),
	})
	if err != nil {
		return nil, storeError("list pending action incidents", err)
	}
	incidents := make([]domain.Incident, 0, len(rows))
	for _, row := range rows {
		incident := incidentFromRow(row)
		if incident.State == domain.StateOpen && incident.RestartOutcome == domain.OutcomePending {
			incidents = append(incidents, incident)
		}
	}
	return incidents, nil
}

func (r *Repository) LoadProgress(ctx context.Context, workloadID string) (ports.WorkloadProgress, error) {
	row, err := r.queries.LoadSupervisorProgress(ctx, workloadID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.WorkloadProgress{}, nil
		}
		return ports.WorkloadProgress{}, storeError("load supervision progress", err)
	}
	return ports.WorkloadProgress{
		WorkloadID: row.WorkloadID, Generation: row.Generation,
		LastState: ports.WorkloadState(row.LastState), LastHealth: row.LastHealth,
		LastExit: row.LastExit, LastRestart: int64(row.LastRestartCount),
		StablePolls: int64(row.StablePolls), ExitOccurrence: int64(row.ExitOccurrence),
		HealthOccurrence: int64(row.HealthOccurrence), OOMOccurrence: int64(row.OomOccurrence),
		PIDsOccurrence: int64(row.PidsOccurrence), FirstSeenAt: row.FirstSeenAt,
	}, nil
}

func (r *Repository) SaveProgress(ctx context.Context, progress ports.WorkloadProgress, now time.Time) error {
	firstSeen := progress.FirstSeenAt
	if firstSeen.IsZero() {
		firstSeen = now
	}
	err := r.queries.UpsertSupervisorProgress(ctx, reliabilitydb.UpsertSupervisorProgressParams{
		WorkloadID: progress.WorkloadID, Generation: progress.Generation,
		LastState: string(progress.LastState), LastHealth: progress.LastHealth,
		LastExit: progress.LastExit, LastRestartCount: int32(progress.LastRestart),
		StablePolls: int32(progress.StablePolls), ExitOccurrence: int32(progress.ExitOccurrence),
		HealthOccurrence: int32(progress.HealthOccurrence), OomOccurrence: int32(progress.OOMOccurrence),
		PidsOccurrence: int32(progress.PIDsOccurrence), FirstSeenAt: firstSeen, UpdatedAt: now,
	})
	if err != nil {
		return storeError("save supervision progress", err)
	}
	return nil
}

func (r *Repository) LoadCheckpoint(ctx context.Context) (time.Time, bool, error) {
	row, err := r.queries.GetSupervisorCheckpoint(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, storeError("load supervision checkpoint", err)
	}
	return row.LastPollAt, true, nil
}

func (r *Repository) SaveCheckpoint(ctx context.Context, at time.Time) error {
	err := r.queries.UpsertSupervisorCheckpoint(ctx, reliabilitydb.UpsertSupervisorCheckpointParams{
		LastPollAt: at, UpdatedAt: at,
	})
	if err != nil {
		return storeError("save supervision checkpoint", err)
	}
	return nil
}

// incidentFromRow mirrors the incident columns of every sqlc row shape
// under the exhaustive-switch discipline the surface module pinned: each
// query that returns a full incident row MUST be listed, and
// TestIncidentRowShapesCarryAcknowledgeKey pins the coverage — a missing
// case would silently drop the acknowledge replay fact.
func incidentFromRow(row any) domain.Incident {
	incident := domain.Incident{}
	switch value := row.(type) {
	case reliabilitydb.GetIncidentRow:
		incident = incidentFromColumns(value.ID, value.OwnerUserID, value.ProjectID,
			value.AppInstanceID, value.AppID, value.WorkloadID, value.WorkloadGeneration,
			value.Violation, value.Summary, value.OccurrenceDigest, value.EvidenceDigest,
			value.State, value.RestartOutcome, value.Revision,
			value.AcknowledgedAt, value.MitigatedAt, value.ResolvedAt, value.CreatedAt, value.UpdatedAt)
		incident.AcknowledgeKey = value.AcknowledgeKey.String
	case reliabilitydb.GetIncidentByOccurrenceRow:
		incident = incidentFromColumns(value.ID, value.OwnerUserID, value.ProjectID,
			value.AppInstanceID, value.AppID, value.WorkloadID, value.WorkloadGeneration,
			value.Violation, value.Summary, value.OccurrenceDigest, value.EvidenceDigest,
			value.State, value.RestartOutcome, value.Revision,
			value.AcknowledgedAt, value.MitigatedAt, value.ResolvedAt, value.CreatedAt, value.UpdatedAt)
		incident.AcknowledgeKey = value.AcknowledgeKey.String
	case reliabilitydb.ListIncidentsPageRow:
		incident = incidentFromColumns(value.ID, value.OwnerUserID, value.ProjectID,
			value.AppInstanceID, value.AppID, value.WorkloadID, value.WorkloadGeneration,
			value.Violation, value.Summary, value.OccurrenceDigest, value.EvidenceDigest,
			value.State, value.RestartOutcome, value.Revision,
			value.AcknowledgedAt, value.MitigatedAt, value.ResolvedAt, value.CreatedAt, value.UpdatedAt)
		incident.AcknowledgeKey = value.AcknowledgeKey.String
	case reliabilitydb.ListOpenIncidentsForWorkloadRow:
		incident = incidentFromColumns(value.ID, value.OwnerUserID, value.ProjectID,
			value.AppInstanceID, value.AppID, value.WorkloadID, value.WorkloadGeneration,
			value.Violation, value.Summary, value.OccurrenceDigest, value.EvidenceDigest,
			value.State, value.RestartOutcome, value.Revision,
			value.AcknowledgedAt, value.MitigatedAt, value.ResolvedAt, value.CreatedAt, value.UpdatedAt)
		incident.AcknowledgeKey = value.AcknowledgeKey.String
	}
	return incident
}

func incidentFromColumns(
	id, ownerUserID, projectID, appInstanceID, appID, workloadID string,
	workloadGeneration int64, violation, summary, occurrenceDigest, evidenceDigest,
	state, restartOutcome string, revision int64,
	acknowledgedAt, mitigatedAt, resolvedAt *time.Time, createdAt, updatedAt time.Time,
) domain.Incident {
	incident := domain.Incident{
		ID: id, OwnerUserID: ownerUserID, ProjectID: projectID,
		AppInstanceID: appInstanceID, AppID: appID,
		WorkloadID: workloadID, WorkloadGeneration: workloadGeneration,
		Violation: domain.Violation(violation), Summary: summary,
		OccurrenceDigest: occurrenceDigest, EvidenceDigest: evidenceDigest,
		State: domain.State(state), RestartOutcome: domain.RestartOutcome(restartOutcome),
		Revision: revision, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
	if acknowledgedAt != nil {
		incident.AcknowledgedAt = acknowledgedAt
	}
	if mitigatedAt != nil {
		incident.MitigatedAt = mitigatedAt
	}
	if resolvedAt != nil {
		incident.ResolvedAt = resolvedAt
	}
	return incident
}

// actionKey derives the durable action key sent to the runtime: a pure
// function of the incident and the action, so a crash between the control
// call and its persistence replays the exact same key.
func actionKey(incidentID, action string) string {
	return "reliability:" + action + ":" + incidentID
}

func storeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", operation, domain.ErrUnavailable, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// textParam maps an empty string to SQL NULL and keeps real values intact.
func textParam(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}
