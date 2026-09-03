// Package application holds the Reliability module's use cases: the
// deterministic supervision loop that turns neutral runtime observations
// into durable, idempotent Incidents and bounded restart/stop decisions
// (ADR-0006 §6), and the owner-scoped incident queries.
package application

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/reliability/domain"
	"github.com/yangtao121/workos/internal/reliability/ports"
)

// Config bounds the supervisor's decision constants.
type Config struct {
	// StablePollsToResolve is how many consecutive healthy observations
	// resolve a mitigated or open incident.
	StablePollsToResolve int64
	// MaxIncidentsPerPoll bounds one poll's incident fan-out.
	MaxIncidentsPerPoll int
}

func (c Config) validate() error {
	if c.StablePollsToResolve < 1 || c.StablePollsToResolve > 1000 {
		return errInvalidSupervisorConfig()
	}
	if c.MaxIncidentsPerPoll < 1 || c.MaxIncidentsPerPoll > 1000 {
		return errInvalidSupervisorConfig()
	}
	return nil
}

type supervisorConfigError struct{}

func errInvalidSupervisorConfig() error { return errors.New("supervisor config out of bounds") }

// Supervisor owns the poll-decide-act loop. It never queries the runtime
// schema, never calls Podman, and never depends on Harness or model
// availability: cgroup hard limits keep enforcing while this loop is down,
// and the durable progress + occurrence digests make every poll at-least-once
// safe.
type Supervisor struct {
	observer   ports.WorkloadObserver
	controller ports.WorkloadController
	repository ports.IncidentRepository
	ids        ids.Generator
	config     Config
	now        func() time.Time
	logger     *slog.Logger
}

func NewSupervisor(
	observer ports.WorkloadObserver,
	controller ports.WorkloadController,
	repository ports.IncidentRepository,
	generator ids.Generator,
	config Config,
	logger *slog.Logger,
) (*Supervisor, error) {
	switch {
	case observer == nil, controller == nil, repository == nil, generator == nil:
		return nil, errors.New("supervisor requires observer, controller, repository, and id generator")
	case logger == nil:
		return nil, errors.New("supervisor requires a logger")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Supervisor{
		observer: observer, controller: controller, repository: repository,
		ids: generator, config: config, now: func() time.Time { return time.Now().UTC() },
		logger: logger,
	}, nil
}

// Poll runs one supervision sweep: observe, classify, report, decide, act,
// and persist progress. A temporarily unreachable runtime aborts the sweep
// with ErrRuntimeUnavailable — the next tick retries; nothing double-fires
// because of it.
func (s *Supervisor) Poll(ctx context.Context) error {
	now := s.now()
	attemptedActions := make(map[string]struct{})
	// Recover decisions whose runtime side effect may have completed before
	// Reliability persisted its own outcome. This queue is independent of
	// the latest observation generation/state, so restart and stop crash
	// windows remain recoverable after the runtime has already moved on.
	if err := s.redrivePendingActions(ctx, now, attemptedActions); err != nil {
		return err
	}
	observations, err := s.observer.ListObservations(ctx)
	if err != nil {
		return ports.ErrRuntimeUnavailable
	}
	budget := s.config.MaxIncidentsPerPoll
	for _, observation := range observations {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.observeOne(ctx, observation, now, &budget, attemptedActions); err != nil {
			s.logger.Warn("supervision sweep workload pending", "error", err)
		}
	}
	if err := s.repository.SaveCheckpoint(ctx, now); err != nil {
		s.logger.Warn("supervision checkpoint pending", "error", err)
	}
	return nil
}

// observeOne classifies one observation against the persisted progress,
// reports exactly one incident per new violation episode, drives the bounded
// decision for open incidents, and advances the stable streak.
func (s *Supervisor) observeOne(ctx context.Context, observation ports.Observation, now time.Time, budget *int, attemptedActions map[string]struct{}) error {
	progress, err := s.repository.LoadProgress(ctx, observation.WorkloadID)
	if err != nil {
		return err
	}
	if progress.WorkloadID == "" {
		progress = ports.WorkloadProgress{
			WorkloadID: observation.WorkloadID, Generation: observation.Generation,
			LastState: ports.StateUnknown, LastHealth: domain.HealthUnknown,
			LastExit: domain.ExitNone, FirstSeenAt: now,
		}
	}
	generationChanged := progress.Generation != observation.Generation
	if generationChanged {
		progress.Generation = observation.Generation
		progress.StablePolls = 0
	}

	type episode struct {
		violation  domain.Violation
		active     bool
		wasActive  bool
		occurrence *int64
	}
	// A classified exit (OOM or pids) is the specific violation: it
	// suppresses the generic unexpected-exit episode so one event reports
	// exactly one incident.
	specificExit := observation.ExitCategory == domain.ExitOOM || observation.ExitCategory == domain.ExitPIDs
	episodes := []episode{
		{domain.ViolationUnexpectedExit, observation.State == ports.StateFailed && !specificExit,
			progress.LastState == ports.StateFailed && progress.LastExit != domain.ExitOOM && progress.LastExit != domain.ExitPIDs,
			&progress.ExitOccurrence},
		{domain.ViolationHealthFailure, observation.State == ports.StateRunning && observation.HealthVerdict == domain.HealthFailing,
			progress.LastState == ports.StateRunning && progress.LastHealth == domain.HealthFailing, &progress.HealthOccurrence},
		{domain.ViolationOOM, observation.ExitCategory == domain.ExitOOM,
			progress.LastExit == domain.ExitOOM, &progress.OOMOccurrence},
		{domain.ViolationPIDsLimit, observation.ExitCategory == domain.ExitPIDs,
			progress.LastExit == domain.ExitPIDs, &progress.PIDsOccurrence},
	}

	for _, item := range episodes {
		if !item.active {
			continue
		}
		// A violation that appears after being absent (or on a new
		// generation) opens a new episode with a fresh ordinal; while it
		// continues, the same occurrence re-reports into the same digest
		// and deduplicates.
		if generationChanged || !item.wasActive {
			*item.occurrence++
		}
		if *budget <= 0 {
			continue
		}
		created, err := s.reportIncident(ctx, observation, item.violation, *item.occurrence, now)
		if err != nil {
			return err
		}
		// MaxIncidentsPerPoll bounds new fan-out, not idempotent replays of
		// already-open episodes. Charging replays would let the same oldest
		// observations consume every poll forever and starve newer workloads.
		if created {
			*budget--
		}
	}

	// Bounded decision for every open incident of this generation: the
	// action ledger's primary key makes each decision idempotent, and the
	// runtime replays the same action key after any crash window.
	if observation.State == ports.StateRunning || observation.State == ports.StateFailed {
		if err := s.decide(ctx, observation, now, attemptedActions); err != nil {
			return err
		}
	}

	// Stable streak: consecutive healthy running observations resolve the
	// generation's incidents — mitigation is the system's fact, resolution
	// needs the streak. Acknowledgement stays the owner's separate fact.
	stable := observation.State == ports.StateRunning && observation.HealthVerdict == domain.HealthOK
	if stable {
		progress.StablePolls++
	} else {
		progress.StablePolls = 0
	}
	if progress.StablePolls >= s.config.StablePollsToResolve {
		if err := s.resolveGenerationIncidents(ctx, observation, now); err != nil {
			return err
		}
	}

	progress.LastState = observation.State
	progress.LastHealth = observation.HealthVerdict
	progress.LastExit = observation.ExitCategory
	progress.LastRestart = observation.RestartCount
	return s.repository.SaveProgress(ctx, progress, now)
}

// reportIncident creates exactly one incident per occurrence digest; the
// unique digest keeps at-least-once replays, duplicate polls, and supervisor
// restarts from double-reporting one episode.
func (s *Supervisor) reportIncident(ctx context.Context, observation ports.Observation, violation domain.Violation, occurrence int64, now time.Time) (bool, error) {
	incident := domain.Incident{
		ID:                 s.ids.New(),
		OwnerUserID:        observation.OwnerUserID,
		ProjectID:          observation.ProjectID,
		AppInstanceID:      observation.AppInstanceID,
		AppID:              observation.AppID,
		WorkloadID:         observation.WorkloadID,
		WorkloadGeneration: observation.Generation,
		Violation:          violation,
		Summary:            violation.Summary(),
		OccurrenceDigest:   domain.OccurrenceDigest(observation.WorkloadID, observation.Generation, violation, occurrence),
		EvidenceDigest: domain.EvidenceDigest(observation.WorkloadID, observation.ManifestDigest,
			string(observation.State), observation.HealthVerdict, observation.ExitCategory),
		State:          domain.StateOpen,
		RestartOutcome: domain.OutcomePending,
		Revision:       1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	created, err := s.repository.CreateIncident(ctx, incident)
	if err != nil {
		return false, err
	}
	if created {
		s.logger.Info("incident reported", "violation", string(violation))
	}
	return created, nil
}

// decide runs the bounded action for the observation's open incidents. The
// first decision is always a restart attempt; the runtime's sanitized refusal
// decides the rest deterministically.
func (s *Supervisor) decide(ctx context.Context, observation ports.Observation, now time.Time, attemptedActions map[string]struct{}) error {
	open, err := s.repository.ListOpenForWorkload(ctx, observation.WorkloadID, observation.Generation)
	if err != nil {
		return err
	}
	for _, incident := range open {
		if incident.RestartOutcome != domain.OutcomePending {
			continue
		}
		if err := s.driveIncident(ctx, incident, now, attemptedActions); err != nil {
			return err
		}
	}
	return nil
}

// redrivePendingActions closes the caller-side crash window around control
// RPCs. It intentionally does not depend on a current observation: after a
// successful restart the observation has a new generation, and after a
// successful stop the workload is already terminal.
func (s *Supervisor) redrivePendingActions(ctx context.Context, now time.Time, attemptedActions map[string]struct{}) error {
	pending, err := s.repository.ListPendingActionIncidents(ctx, s.config.MaxIncidentsPerPoll)
	if err != nil {
		return err
	}
	for _, incident := range pending {
		if err := s.driveIncident(ctx, incident, now, attemptedActions); err != nil {
			return err
		}
	}
	return nil
}

func (s *Supervisor) driveIncident(ctx context.Context, incident domain.Incident, now time.Time, attemptedActions map[string]struct{}) error {
	if incident.RestartOutcome != domain.OutcomePending || incident.State != domain.StateOpen {
		return nil
	}
	// The dedicated budget incident owns the one deterministic stop key. If
	// the supervisor crashes anywhere around that RPC, the pending queue
	// replays this exact action independently of workload state.
	if incident.Violation == domain.ViolationRestartLimit {
		stop, err := s.stopWorkload(ctx, incident, now, attemptedActions)
		if err != nil {
			return err
		}
		switch stop.Outcome {
		case ports.ControlStopped:
			return s.updateOutcome(ctx, incident.ID, domain.StateMitigated, domain.OutcomeStopped, now)
		case ports.ControlUnavailable:
			return nil
		default:
			// A sanitized permanent refusal is an owner-visible failed
			// decision, not an action that should occupy the pending queue
			// forever.
			return s.updateOutcome(ctx, incident.ID, domain.StateOpen, domain.OutcomeFailed, now)
		}
	}
	stored, err := s.repository.LookupAction(ctx, incident.ID, "restart")
	if err != nil {
		return err
	}
	if stored.IncidentID == "" || stored.Outcome == ports.ControlUnavailable {
		actionKey := "reliability:restart:" + incident.ID
		if _, attempted := attemptedActions[actionKey]; attempted {
			return nil
		}
		attemptedActions[actionKey] = struct{}{}
		result, err := s.controller.Restart(ctx, incident.WorkloadID, actionKey)
		if err != nil {
			return err
		}
		if err := s.repository.RecordAction(ctx, incident.ID, "restart", result, now); err != nil {
			return err
		}
		stored, err = s.repository.LookupAction(ctx, incident.ID, "restart")
		if err != nil {
			return err
		}
		if stored.IncidentID == "" {
			return domain.ErrUnavailable
		}
	}
	switch stored.Outcome {
	case ports.ControlRestarted:
		if err := s.updateOutcome(ctx, incident.ID, domain.StateMitigated, domain.OutcomeRestarted, now); err != nil {
			return err
		}
	case ports.ControlLimitExhausted:
		// The restart budget is spent: report the budget incident once,
		// then stop under the budget incident's terminate action key. The
		// episode closes as stopped only when the stop actually
		// succeeded; an unavailable stop stays pending and re-drives on
		// the next poll (the crash window between reporting and stopping
		// replays the same keys).
		budget, err := s.reportLimitExhausted(ctx, incident, now)
		if err != nil {
			return err
		}
		stop, err := s.stopWorkload(ctx, budget, now, attemptedActions)
		if err != nil {
			return err
		}
		if stop.Outcome != ports.ControlStopped {
			if stop.Outcome != ports.ControlUnavailable {
				if budget.State == domain.StateOpen {
					if err := s.updateOutcome(ctx, budget.ID, domain.StateOpen, domain.OutcomeFailed, now); err != nil {
						return err
					}
				}
				return s.updateOutcome(ctx, incident.ID, domain.StateOpen, domain.OutcomeFailed, now)
			}
			return nil
		}
		if err := s.updateOutcome(ctx, incident.ID, domain.StateMitigated, domain.OutcomeStopped, now); err != nil {
			return err
		}
		if budget.State == domain.StateOpen {
			if err := s.updateOutcome(ctx, budget.ID, domain.StateMitigated, domain.OutcomeStopped, now); err != nil {
				return err
			}
		}
	case ports.ControlStopped:
		if err := s.updateOutcome(ctx, incident.ID, domain.StateMitigated, domain.OutcomeStopped, now); err != nil {
			return err
		}
	case ports.ControlUnsupported, ports.ControlConflict, ports.ControlFailed:
		// The workload is not restartable in its current state; the
		// episode stays open for the owner instead of looping.
		if err := s.updateOutcome(ctx, incident.ID, domain.StateOpen, domain.OutcomeFailed, now); err != nil {
			return err
		}
	case ports.ControlUnavailable:
		// Retryable: leave pending; the next poll re-drives with the same
		// action key.
	}
	return nil
}

func (s *Supervisor) updateOutcome(ctx context.Context, incidentID string, state domain.State, outcome domain.RestartOutcome, now time.Time) error {
	err := s.repository.UpdateOutcome(ctx, incidentID, state, outcome, now)
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	current, getErr := s.repository.GetIncident(ctx, incidentID)
	if getErr == nil && current.State == state && current.RestartOutcome == outcome {
		return nil
	}
	return err
}

// reportLimitExhausted creates the restart-budget incident (one per
// workload+generation) when the runtime refuses a restart for the budget.
func (s *Supervisor) reportLimitExhausted(ctx context.Context, incident domain.Incident, now time.Time) (domain.Incident, error) {
	return s.reportIncidentForWorkload(ctx, incident, domain.ViolationRestartLimit, 1, now)
}

func (s *Supervisor) reportIncidentForWorkload(ctx context.Context, source domain.Incident, violation domain.Violation, occurrence int64, now time.Time) (domain.Incident, error) {
	incident := domain.Incident{
		ID:                 s.ids.New(),
		OwnerUserID:        source.OwnerUserID,
		ProjectID:          source.ProjectID,
		AppInstanceID:      source.AppInstanceID,
		AppID:              source.AppID,
		WorkloadID:         source.WorkloadID,
		WorkloadGeneration: source.WorkloadGeneration,
		Violation:          violation,
		Summary:            violation.Summary(),
		OccurrenceDigest:   domain.OccurrenceDigest(source.WorkloadID, source.WorkloadGeneration, violation, occurrence),
		EvidenceDigest:     domain.EvidenceDigest(source.WorkloadID, string(violation)),
		State:              domain.StateOpen,
		RestartOutcome:     domain.OutcomePending,
		Revision:           1,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	created, err := s.repository.CreateIncident(ctx, incident)
	if err != nil {
		return domain.Incident{}, err
	}
	// The stop decision runs under the budget incident's terminate action key:
	// creating the incident and executing the stop are two
	// idempotent steps, so a crash between them replays both instead of
	// reporting a stopped workload that was never stopped.
	_ = created
	return s.repository.GetIncidentByOccurrence(ctx, incident.OccurrenceDigest)
}

// stopWorkload executes (or replays) the incident's terminate action and
// returns the sanitized outcome. It never interprets the outcome: the caller
// decides what a stopped/unavailable verdict means for the episode.
func (s *Supervisor) stopWorkload(ctx context.Context, incident domain.Incident, now time.Time, attemptedActions map[string]struct{}) (ports.ControlResult, error) {
	stored, err := s.repository.LookupAction(ctx, incident.ID, "terminate")
	if err != nil {
		return ports.ControlResult{}, err
	}
	if stored.IncidentID != "" && stored.Outcome != ports.ControlUnavailable {
		return ports.ControlResult{Outcome: stored.Outcome, Generation: stored.ResultGeneration}, nil
	}
	actionKey := "reliability:stop:" + incident.ID
	if _, attempted := attemptedActions[actionKey]; attempted {
		return ports.ControlResult{Outcome: ports.ControlUnavailable}, nil
	}
	attemptedActions[actionKey] = struct{}{}
	result, err := s.controller.Stop(ctx, incident.WorkloadID, actionKey, "restart_limit")
	if err != nil {
		return ports.ControlResult{}, err
	}
	if err := s.repository.RecordAction(ctx, incident.ID, "terminate", result, now); err != nil {
		return ports.ControlResult{}, err
	}
	stored, err = s.repository.LookupAction(ctx, incident.ID, "terminate")
	if err != nil {
		return ports.ControlResult{}, err
	}
	if stored.IncidentID == "" {
		return ports.ControlResult{}, domain.ErrUnavailable
	}
	return ports.ControlResult{Outcome: stored.Outcome, Generation: stored.ResultGeneration}, nil
}

func (s *Supervisor) resolveGenerationIncidents(ctx context.Context, observation ports.Observation, now time.Time) error {
	open, err := s.repository.ListMitigatedForWorkload(ctx, observation.WorkloadID, observation.Generation)
	if err != nil {
		return err
	}
	for _, incident := range open {
		if err := s.repository.MarkResolved(ctx, incident.ID, now); err != nil {
			return err
		}
	}
	return nil
}
