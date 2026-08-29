package application

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/reliability/domain"
	"github.com/yangtao121/workos/internal/reliability/ports"
)

// The fakes mirror the durable semantics: unique occurrence digests, the
// (incident, action) ledger, and the supervision progress.

func testLogger2() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeObserver struct {
	observations []ports.Observation
	err          error
}

func (f *fakeObserver) ListObservations(context.Context) ([]ports.Observation, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.observations, nil
}

type fakeController struct {
	mu            sync.Mutex
	restarts      []string
	stops         []string
	restartResult ports.ControlResult
}

func (c *fakeController) Restart(_ context.Context, workloadID, actionKey string) (ports.ControlResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restarts = append(c.restarts, actionKey)
	return c.restartResult, nil
}

func (c *fakeController) Stop(_ context.Context, workloadID, actionKey, _ string) (ports.ControlResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stops = append(c.stops, actionKey)
	return ports.ControlResult{Outcome: ports.ControlStopped}, nil
}

type fakeIncidentRepo struct {
	mu            sync.Mutex
	incidents     map[string]domain.Incident
	digests       map[string]string // occurrence digest → incident id
	actions       map[string]ports.StoredAction
	progress      map[string]ports.WorkloadProgress
	checkpoint    time.Time
	hasCheckpoint bool
}

func newFakeIncidentRepo() *fakeIncidentRepo {
	return &fakeIncidentRepo{
		incidents: map[string]domain.Incident{},
		digests:   map[string]string{},
		actions:   map[string]ports.StoredAction{},
		progress:  map[string]ports.WorkloadProgress{},
	}
}

func ledgerKey(incidentID, action string) string { return incidentID + "|" + action }

func (r *fakeIncidentRepo) CreateIncident(_ context.Context, incident domain.Incident) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.digests[incident.OccurrenceDigest]; ok {
		incident = r.incidents[existing]
		return false, nil
	}
	r.incidents[incident.ID] = incident
	r.digests[incident.OccurrenceDigest] = incident.ID
	return true, nil
}

func (r *fakeIncidentRepo) GetIncident(_ context.Context, incidentID string) (domain.Incident, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	incident, ok := r.incidents[incidentID]
	if !ok {
		return domain.Incident{}, domain.ErrNotFound
	}
	return incident, nil
}

func (r *fakeIncidentRepo) ListIncidents(context.Context, ports.IncidentFilter, int) ([]domain.Incident, error) {
	return nil, nil
}

func (r *fakeIncidentRepo) UpdateOutcome(_ context.Context, incidentID string, state domain.State, outcome domain.RestartOutcome, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	incident, ok := r.incidents[incidentID]
	if !ok {
		return domain.ErrNotFound
	}
	incident.State = state
	incident.RestartOutcome = outcome
	incident.Revision++
	incident.UpdatedAt = now
	if state == domain.StateMitigated && incident.MitigatedAt == nil {
		stamped := now
		incident.MitigatedAt = &stamped
	}
	r.incidents[incidentID] = incident
	return nil
}

func (r *fakeIncidentRepo) MarkResolved(_ context.Context, incidentID string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	incident, ok := r.incidents[incidentID]
	if !ok {
		return domain.ErrNotFound
	}
	incident.State = domain.StateResolved
	incident.Revision++
	stamped := now
	incident.ResolvedAt = &stamped
	incident.UpdatedAt = now
	r.incidents[incidentID] = incident
	return nil
}

func (r *fakeIncidentRepo) Acknowledge(_ context.Context, incidentID, _ string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	incident, ok := r.incidents[incidentID]
	if !ok {
		return domain.ErrNotFound
	}
	if incident.AcknowledgedAt == nil {
		stamped := now
		incident.AcknowledgedAt = &stamped
		incident.Revision++
		incident.UpdatedAt = now
		r.incidents[incidentID] = incident
	}
	return nil
}

func (r *fakeIncidentRepo) ListOpenForWorkload(_ context.Context, workloadID string, generation int64) ([]domain.Incident, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []domain.Incident
	for _, incident := range r.incidents {
		if incident.WorkloadID == workloadID && incident.WorkloadGeneration == generation &&
			incident.State != domain.StateResolved {
			result = append(result, incident)
		}
	}
	return result, nil
}

func (r *fakeIncidentRepo) RecordAction(_ context.Context, incidentID, action string, result ports.ControlResult, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := ledgerKey(incidentID, action)
	existing, ok := r.actions[key]
	if !ok {
		existing = ports.StoredAction{IncidentID: incidentID, Action: action}
	}
	outcome := result.Outcome
	if existing.Outcome != "" && outcome == ports.ControlUnavailable {
		// A replayed unavailable verdict never erases a recorded one.
		outcome = existing.Outcome
	}
	r.actions[key] = ports.StoredAction{
		IncidentID: incidentID, Action: action, Outcome: outcome,
		ResultGeneration: result.Generation,
	}
	_ = now
	return nil
}

func (r *fakeIncidentRepo) LookupAction(_ context.Context, incidentID, action string) (ports.StoredAction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.actions[ledgerKey(incidentID, action)]
	if !ok {
		return ports.StoredAction{}, nil
	}
	return stored, nil
}

func (r *fakeIncidentRepo) ListPendingActionIncidents(context.Context, int) ([]domain.Incident, error) {
	return nil, nil
}

func (r *fakeIncidentRepo) LoadProgress(_ context.Context, workloadID string) (ports.WorkloadProgress, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.progress[workloadID], nil
}

func (r *fakeIncidentRepo) SaveProgress(_ context.Context, progress ports.WorkloadProgress, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress[progress.WorkloadID] = progress
	return nil
}

func (r *fakeIncidentRepo) LoadCheckpoint(context.Context) (time.Time, bool, error) {
	return r.checkpoint, r.hasCheckpoint, nil
}

func (r *fakeIncidentRepo) SaveCheckpoint(_ context.Context, at time.Time) error {
	r.checkpoint = at
	r.hasCheckpoint = true
	return nil
}

func newSupervisor(t *testing.T, observer ports.WorkloadObserver, controller ports.WorkloadController, repo ports.IncidentRepository) *Supervisor {
	t.Helper()
	supervisor, err := NewSupervisor(observer, controller, repo, ids.UUIDv7{}, Config{
		StablePollsToResolve: 3, MaxIncidentsPerPoll: 8,
	}, testLogger2())
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	return supervisor
}

func testObservation(state ports.WorkloadState, health string, exit string) ports.Observation {
	return ports.Observation{
		WorkloadID: "0198d7ea-2110-7c42-b659-c5e4d73bc351", OwnerUserID: testOwnerID,
		ProjectID: testProjectID, AppInstanceID: testInstanceID, AppID: "notes-app",
		ManifestDigest: "sha256:" + repeat('a', 64),
		Generation:     1, State: state, RestartCount: 0,
		HealthVerdict: health, ExitCategory: exit, ObservedAt: time.Now().UTC(),
	}
}

const (
	testOwnerID    = "0198d7ea-2110-7c42-b659-c5e4d73bc341"
	testProjectID  = "0198d7ea-2110-7c42-b659-c5e4d73bc342"
	testInstanceID = "0198d7ea-2110-7c42-b659-c5e4d73bc343"
)

func repeat(char rune, count int) string {
	result := make([]rune, count)
	for index := range result {
		result[index] = char
	}
	return string(result)
}

func findIncident(t *testing.T, repo *fakeIncidentRepo, violation domain.Violation) domain.Incident {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, incident := range repo.incidents {
		if incident.Violation == violation {
			return incident
		}
	}
	t.Fatalf("no %s incident", violation)
	return domain.Incident{}
}

// TestPollReportsOneIncidentPerEpisodeAndRestartsOnce pins the at-least-once
// contract: repeated observations of the same violation episode produce
// exactly one incident and exactly one restart; recovery then a new failure
// opens a second occurrence.
func TestPollReportsOneIncidentPerEpisodeAndRestartsOnce(t *testing.T) {
	observer := &fakeObserver{}
	controller := &fakeController{restartResult: ports.ControlResult{Outcome: ports.ControlRestarted, Generation: 2}}
	repo := newFakeIncidentRepo()
	supervisor := newSupervisor(t, observer, controller, repo)
	ctx := context.Background()

	observation := testObservation(ports.StateFailed, domain.HealthFailing, "exited")
	observer.observations = []ports.Observation{observation}
	for poll := 0; poll < 3; poll++ {
		if err := supervisor.Poll(ctx); err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
	}
	if len(repo.incidents) != 1 {
		t.Fatalf("incidents %d, want exactly 1 for one episode", len(repo.incidents))
	}
	incident := findIncident(t, repo, domain.ViolationUnexpectedExit)
	if incident.State != domain.StateMitigated || incident.RestartOutcome != domain.OutcomeRestarted {
		t.Fatalf("incident state %v outcome %v, want mitigated/restarted", incident.State, incident.RestartOutcome)
	}
	if len(controller.restarts) != 1 {
		t.Fatalf("restarts %d, want exactly 1 across repeated polls", len(controller.restarts))
	}

	// Recovery: healthy running observations ride the stable streak, then the
	// incident resolves.
	observer.observations = []ports.Observation{testObservation(ports.StateRunning, domain.HealthOK, domain.ExitNone)}
	for poll := 0; poll < 3; poll++ {
		if err := supervisor.Poll(ctx); err != nil {
			t.Fatalf("recovery poll %d: %v", poll, err)
		}
	}
	incident = findIncident(t, repo, domain.ViolationUnexpectedExit)
	if incident.State != domain.StateResolved {
		t.Fatalf("incident state %v after stable streak, want resolved", incident.State)
	}

	// A new failure after recovery is a new occurrence with its own incident.
	observer.observations = []ports.Observation{observation}
	if err := supervisor.Poll(ctx); err != nil {
		t.Fatalf("second episode poll: %v", err)
	}
	if len(repo.incidents) != 2 {
		t.Fatalf("incidents %d after a fresh episode, want 2", len(repo.incidents))
	}
	if len(controller.restarts) != 2 {
		t.Fatalf("restarts %d after a fresh episode, want 2", len(controller.restarts))
	}
}

// TestPollCreatesSeparateIncidentsPerViolation pins the classification: an
// OOM exit reports the OOM violation, and health failures report theirs.
func TestPollCreatesSeparateIncidentsPerViolation(t *testing.T) {
	observer := &fakeObserver{}
	controller := &fakeController{restartResult: ports.ControlResult{Outcome: ports.ControlRestarted, Generation: 2}}
	repo := newFakeIncidentRepo()
	supervisor := newSupervisor(t, observer, controller, repo)

	observer.observations = []ports.Observation{
		testObservation(ports.StateFailed, domain.HealthFailing, domain.ExitOOM),
	}
	if err := supervisor.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(repo.incidents) != 1 {
		t.Fatalf("incidents %d, want 1 OOM incident", len(repo.incidents))
	}
	incident := findIncident(t, repo, domain.ViolationOOM)
	if incident.Violation.Severity() != domain.SeverityCritical {
		t.Fatalf("OOM severity %v, want critical", incident.Violation.Severity())
	}
	if incident.Summary == "" || len(incident.Summary) > 200 {
		t.Fatalf("summary %q is not the fixed phrase", incident.Summary)
	}
}

// TestPollStopsAfterRestartLimit pins the crash-loop bound: the runtime's
// limit-exhausted refusal reports the violation once, stops the workload,
// and closes the original episode as stopped — never an infinite loop.
func TestPollStopsAfterRestartLimit(t *testing.T) {
	observer := &fakeObserver{}
	controller := &fakeController{restartResult: ports.ControlResult{Outcome: ports.ControlLimitExhausted}}
	repo := newFakeIncidentRepo()
	supervisor := newSupervisor(t, observer, controller, repo)

	observer.observations = []ports.Observation{
		testObservation(ports.StateFailed, domain.HealthFailing, "exited"),
	}
	for poll := 0; poll < 3; poll++ {
		if err := supervisor.Poll(context.Background()); err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
	}
	findIncident(t, repo, domain.ViolationRestartLimit)
	if len(controller.stops) != 1 {
		t.Fatalf("stops %d, want exactly 1", len(controller.stops))
	}
	incident := findIncident(t, repo, domain.ViolationUnexpectedExit)
	if incident.RestartOutcome != domain.OutcomeStopped {
		t.Fatalf("original outcome %v, want stopped", incident.RestartOutcome)
	}
}

// TestPollRuntimeUnavailableLeavesNothingBehind pins the outage behavior: an
// unreachable runtime aborts the sweep, persists nothing partial, and the
// next successful poll converges.
func TestPollRuntimeUnavailableLeavesNothingBehind(t *testing.T) {
	observer := &fakeObserver{}
	controller := &fakeController{restartResult: ports.ControlResult{Outcome: ports.ControlRestarted, Generation: 2}}
	repo := newFakeIncidentRepo()
	supervisor := newSupervisor(t, observer, controller, repo)

	observer.err = errUnavailable
	if err := supervisor.Poll(context.Background()); err == nil {
		t.Fatalf("poll with unreachable runtime, want error")
	}
	if len(repo.incidents) != 0 {
		t.Fatalf("incidents %d after an aborted sweep, want 0", len(repo.incidents))
	}
	observer.err = nil
	observer.observations = []ports.Observation{
		testObservation(ports.StateFailed, domain.HealthFailing, "exited"),
	}
	if err := supervisor.Poll(context.Background()); err != nil {
		t.Fatalf("recovery poll: %v", err)
	}
	if len(repo.incidents) != 1 {
		t.Fatalf("incidents %d after recovery, want 1", len(repo.incidents))
	}
}

type errUnavailableError struct{}

func (errUnavailableError) Error() string { return "unavailable" }

var errUnavailable = errUnavailableError{}
