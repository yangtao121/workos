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
	stopOutcome   ports.ControlOutcome
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
	return ports.ControlResult{Outcome: c.stopOutcome}, nil
}

type fakeIncidentRepo struct {
	mu                      sync.Mutex
	incidents               map[string]domain.Incident
	digests                 map[string]string // occurrence digest → incident id
	actions                 map[string]ports.StoredAction
	progress                map[string]ports.WorkloadProgress
	checkpoint              time.Time
	hasCheckpoint           bool
	failActionOnce          string
	authoritativeActionOnce string
	authoritativeResult     ports.ControlResult
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

func (r *fakeIncidentRepo) GetIncidentByOccurrence(_ context.Context, digest string) (domain.Incident, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.digests[digest]; ok {
		return r.incidents[id], nil
	}
	return domain.Incident{}, domain.ErrNotFound
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
	if !ok || incident.State != domain.StateOpen || incident.RestartOutcome != domain.OutcomePending {
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
	if !ok || incident.State != domain.StateMitigated {
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

func (r *fakeIncidentRepo) Acknowledge(_ context.Context, incidentID, _ string, _ string, now time.Time) error {
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

func (r *fakeIncidentRepo) ListMitigatedForWorkload(_ context.Context, workloadID string, throughGeneration int64) ([]domain.Incident, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []domain.Incident
	for _, incident := range r.incidents {
		if incident.WorkloadID == workloadID && incident.WorkloadGeneration <= throughGeneration &&
			incident.State == domain.StateMitigated {
			result = append(result, incident)
		}
	}
	return result, nil
}

func (r *fakeIncidentRepo) RecordAction(_ context.Context, incidentID, action string, result ports.ControlResult, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failActionOnce == action {
		r.failActionOnce = ""
		return errUnavailable
	}
	key := ledgerKey(incidentID, action)
	if r.authoritativeActionOnce == action {
		r.authoritativeActionOnce = ""
		r.actions[key] = ports.StoredAction{
			IncidentID: incidentID, Action: action, Outcome: r.authoritativeResult.Outcome,
			ResultGeneration: r.authoritativeResult.Generation,
		}
	}
	existing, ok := r.actions[key]
	if !ok {
		existing = ports.StoredAction{IncidentID: incidentID, Action: action}
	}
	outcome := result.Outcome
	if existing.Outcome != "" && existing.Outcome != ports.ControlUnavailable {
		// Terminal runtime verdicts are immutable; only an unavailable
		// attempt may be replaced by a replay result.
		outcome = existing.Outcome
		result.Generation = existing.ResultGeneration
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

func (r *fakeIncidentRepo) ListPendingActionIncidents(_ context.Context, limit int) ([]domain.Incident, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]domain.Incident, 0, limit)
	// Budget incidents first mirrors the recovery-safe production ordering:
	// their stop result can then settle any source episode in the same sweep.
	for _, budgetOnly := range []bool{true, false} {
		for _, incident := range r.incidents {
			isBudget := incident.Violation == domain.ViolationRestartLimit
			if isBudget != budgetOnly || incident.State != domain.StateOpen || incident.RestartOutcome != domain.OutcomePending {
				continue
			}
			result = append(result, incident)
			if len(result) >= limit {
				return result, nil
			}
		}
	}
	return result, nil
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
	recovered := testObservation(ports.StateRunning, domain.HealthOK, domain.ExitNone)
	recovered.Generation = 2
	observer.observations = []ports.Observation{recovered}
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

func TestPollBudgetDoesNotLetExistingEpisodeStarveNewWorkload(t *testing.T) {
	repository := newFakeIncidentRepo()
	observer := &fakeObserver{}
	controller := &fakeController{restartResult: ports.ControlResult{Outcome: ports.ControlUnsupported}}
	first := testObservation(ports.StateFailed, domain.HealthFailing, "exited")
	second := first
	second.WorkloadID = "0198d7ea-2110-7c42-b659-c5e4d73bc399"
	observer.observations = []ports.Observation{first, second}
	supervisor, err := NewSupervisor(observer, controller, repository, ids.UUIDv7{}, Config{
		StablePollsToResolve: 3,
		MaxIncidentsPerPoll:  1,
	}, testLogger2())
	if err != nil {
		t.Fatal(err)
	}

	if err := supervisor.Poll(context.Background()); err != nil {
		t.Fatalf("first bounded poll: %v", err)
	}
	if got := len(repository.incidents); got != 1 {
		t.Fatalf("first poll incidents = %d, want 1", got)
	}
	if err := supervisor.Poll(context.Background()); err != nil {
		t.Fatalf("second bounded poll: %v", err)
	}
	if got := len(repository.incidents); got != 2 {
		t.Fatalf("existing episode consumed the second poll budget: incidents = %d, want 2", got)
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
	controller := &fakeController{
		restartResult: ports.ControlResult{Outcome: ports.ControlLimitExhausted},
		stopOutcome:   ports.ControlStopped,
	}
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

// TestPollStopUnavailableStaysPendingAndRedrives pins the stop-semantics
// repair: a stop that reports unavailable leaves the incident open and
// pending (never marked stopped), and the next poll re-drives the same
// action key to a verifiable stop.
func TestPollStopUnavailableStaysPendingAndRedrives(t *testing.T) {
	observer := &fakeObserver{}
	controller := &fakeController{restartResult: ports.ControlResult{Outcome: ports.ControlRestarted, Generation: 2}}
	repo := newFakeIncidentRepo()
	supervisor := newSupervisor(t, observer, controller, repo)

	observer.observations = []ports.Observation{
		testObservation(ports.StateFailed, domain.HealthFailing, "exited"),
	}
	// First poll: restart fails at the runtime with the budget spent and the
	// stop is unavailable — nothing may claim "stopped".
	controller.restartResult = ports.ControlResult{Outcome: ports.ControlLimitExhausted}
	controller.stopOutcome = ports.ControlUnavailable
	if err := supervisor.Poll(context.Background()); err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	incident := findIncident(t, repo, domain.ViolationUnexpectedExit)
	if incident.RestartOutcome == domain.OutcomeStopped || incident.State == domain.StateMitigated {
		t.Fatalf("unavailable stop marked the episode stopped: %+v", incident)
	}
	if incident.State != domain.StateOpen || incident.RestartOutcome != domain.OutcomePending {
		t.Fatalf("incident must stay open/pending after an unavailable stop: %+v", incident)
	}
	if len(controller.stops) != 1 {
		t.Fatalf("first poll issued %d stops, want one", len(controller.stops))
	}
	// A full recovery poll may see both the source and budget incidents, but
	// their shared stop key is attempted only once in that poll.
	if err := supervisor.Poll(context.Background()); err != nil {
		t.Fatalf("poll 2 unavailable: %v", err)
	}
	if len(controller.stops) != 2 {
		t.Fatalf("unavailable recovery issued %d stops, want one attempt per poll", len(controller.stops))
	}
	// Third poll with the stop now succeeding: the same action key re-drives
	// and the episode closes as genuinely stopped.
	controller.stopOutcome = ports.ControlStopped
	if err := supervisor.Poll(context.Background()); err != nil {
		t.Fatalf("poll 3: %v", err)
	}
	incident = findIncident(t, repo, domain.ViolationUnexpectedExit)
	if incident.RestartOutcome != domain.OutcomeStopped {
		t.Fatalf("episode outcome %v after a successful stop, want stopped", incident.RestartOutcome)
	}
	budget := findIncident(t, repo, domain.ViolationRestartLimit)
	if budget.RestartOutcome != domain.OutcomeStopped {
		t.Fatalf("budget incident outcome %v, want stopped", budget.RestartOutcome)
	}
	// Exactly one physical stop hit the controller: the retry replayed the
	// recorded unavailable action instead of re-issuing a stop.
	if len(controller.stops) != 3 {
		t.Fatalf("stops %d, want one attempt in each of three polls", len(controller.stops))
	}
}

func TestPollPermanentRestartFailureDoesNotRedrive(t *testing.T) {
	observer := &fakeObserver{observations: []ports.Observation{
		testObservation(ports.StateFailed, domain.HealthFailing, "exited"),
	}}
	controller := &fakeController{restartResult: ports.ControlResult{Outcome: ports.ControlFailed}}
	repo := newFakeIncidentRepo()
	supervisor := newSupervisor(t, observer, controller, repo)

	for poll := 0; poll < 2; poll++ {
		if err := supervisor.Poll(context.Background()); err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
	}
	incident := findIncident(t, repo, domain.ViolationUnexpectedExit)
	if incident.State != domain.StateOpen || incident.RestartOutcome != domain.OutcomeFailed {
		t.Fatalf("permanent restart verdict did not close the pending decision: %+v", incident)
	}
	if len(controller.restarts) != 1 {
		t.Fatalf("permanent restart failure was re-driven %d times", len(controller.restarts))
	}
}

func TestPollUsesAuthoritativeActionLedgerAfterConcurrentTerminalWrite(t *testing.T) {
	observer := &fakeObserver{observations: []ports.Observation{
		testObservation(ports.StateFailed, domain.HealthFailing, "exited"),
	}}
	controller := &fakeController{restartResult: ports.ControlResult{Outcome: ports.ControlUnavailable}}
	repo := newFakeIncidentRepo()
	repo.authoritativeActionOnce = "restart"
	repo.authoritativeResult = ports.ControlResult{Outcome: ports.ControlRestarted, Generation: 2}
	supervisor := newSupervisor(t, observer, controller, repo)

	if err := supervisor.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	incident := findIncident(t, repo, domain.ViolationUnexpectedExit)
	if incident.State != domain.StateMitigated || incident.RestartOutcome != domain.OutcomeRestarted {
		t.Fatalf("caller result overrode authoritative ledger: %+v", incident)
	}
}

func TestPollPermanentStopFailureDoesNotRedrive(t *testing.T) {
	observer := &fakeObserver{observations: []ports.Observation{
		testObservation(ports.StateFailed, domain.HealthFailing, "exited"),
	}}
	controller := &fakeController{
		restartResult: ports.ControlResult{Outcome: ports.ControlLimitExhausted},
		stopOutcome:   ports.ControlFailed,
	}
	repo := newFakeIncidentRepo()
	supervisor := newSupervisor(t, observer, controller, repo)

	for poll := 0; poll < 2; poll++ {
		if err := supervisor.Poll(context.Background()); err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
	}
	source := findIncident(t, repo, domain.ViolationUnexpectedExit)
	budget := findIncident(t, repo, domain.ViolationRestartLimit)
	if source.RestartOutcome != domain.OutcomeFailed || budget.RestartOutcome != domain.OutcomeFailed {
		t.Fatalf("terminal stop failure left a pending decision: source=%+v budget=%+v", source, budget)
	}
	if len(controller.restarts) != 1 || len(controller.stops) != 1 {
		t.Fatalf("terminal control calls repeated: restarts=%d stops=%d", len(controller.restarts), len(controller.stops))
	}
}

// TestPollBudgetIncidentNeverRestarts pins the decision guard: the
// restart-budget incident's decision is a stop, never another restart —
// there is no path back into the spent budget.
func TestPollBudgetIncidentNeverRestarts(t *testing.T) {
	observer := &fakeObserver{}
	controller := &fakeController{restartResult: ports.ControlResult{Outcome: ports.ControlRestarted, Generation: 2}}
	repo := newFakeIncidentRepo()
	supervisor := newSupervisor(t, observer, controller, repo)

	observer.observations = []ports.Observation{
		testObservation(ports.StateFailed, domain.HealthFailing, "exited"),
	}
	controller.restartResult = ports.ControlResult{Outcome: ports.ControlLimitExhausted}
	controller.stopOutcome = ports.ControlStopped
	for poll := 0; poll < 3; poll++ {
		if err := supervisor.Poll(context.Background()); err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
	}
	if len(controller.restarts) != 1 {
		t.Fatalf("restarts %d, want exactly 1 (the original episode); the budget incident must never re-enter the spent budget", len(controller.restarts))
	}
	budget := findIncident(t, repo, domain.ViolationRestartLimit)
	if budget.RestartOutcome != domain.OutcomeStopped {
		t.Fatalf("budget incident outcome %v, want settled stopped by the source stop", budget.RestartOutcome)
	}
	if len(controller.stops) != 1 {
		t.Fatalf("stops %d, want exactly 1 (single stop authority)", len(controller.stops))
	}
}

func TestPollRedrivesRestartAfterRuntimeSucceededBeforeActionCommit(t *testing.T) {
	observer := &fakeObserver{}
	controller := &fakeController{restartResult: ports.ControlResult{Outcome: ports.ControlRestarted, Generation: 2}}
	repo := newFakeIncidentRepo()
	repo.failActionOnce = "restart"
	supervisor := newSupervisor(t, observer, controller, repo)
	observer.observations = []ports.Observation{testObservation(ports.StateFailed, domain.HealthFailing, "exited")}
	if err := supervisor.Poll(context.Background()); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	incident := findIncident(t, repo, domain.ViolationUnexpectedExit)
	if incident.State != domain.StateOpen || incident.RestartOutcome != domain.OutcomePending {
		t.Fatalf("crash-window incident %+v, want open/pending", incident)
	}
	recovered := testObservation(ports.StateRunning, domain.HealthOK, domain.ExitNone)
	recovered.Generation = 2
	observer.observations = []ports.Observation{recovered}
	if err := supervisor.Poll(context.Background()); err != nil {
		t.Fatalf("recovery poll: %v", err)
	}
	incident = findIncident(t, repo, domain.ViolationUnexpectedExit)
	if incident.State != domain.StateMitigated || incident.RestartOutcome != domain.OutcomeRestarted {
		t.Fatalf("recovered incident %+v", incident)
	}
	if len(controller.restarts) != 2 || controller.restarts[0] != controller.restarts[1] {
		t.Fatalf("restart replay keys %v, want two identical calls", controller.restarts)
	}
	// The healthy replacement resolves the repaired old-generation incident.
	for poll := 0; poll < 2; poll++ {
		if err := supervisor.Poll(context.Background()); err != nil {
			t.Fatalf("stable poll %d: %v", poll, err)
		}
	}
	incident = findIncident(t, repo, domain.ViolationUnexpectedExit)
	if incident.State != domain.StateResolved {
		t.Fatalf("old-generation incident state %v, want resolved", incident.State)
	}
}

func TestPollRedrivesStopAfterRuntimeSucceededBeforeActionCommit(t *testing.T) {
	observer := &fakeObserver{}
	controller := &fakeController{
		restartResult: ports.ControlResult{Outcome: ports.ControlLimitExhausted},
		stopOutcome:   ports.ControlStopped,
	}
	repo := newFakeIncidentRepo()
	repo.failActionOnce = "terminate"
	supervisor := newSupervisor(t, observer, controller, repo)
	observer.observations = []ports.Observation{testObservation(ports.StateFailed, domain.HealthFailing, "exited")}
	if err := supervisor.Poll(context.Background()); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	observer.observations = []ports.Observation{testObservation(ports.StateStopped, domain.HealthFailing, "exited")}
	if err := supervisor.Poll(context.Background()); err != nil {
		t.Fatalf("recovery poll: %v", err)
	}
	source := findIncident(t, repo, domain.ViolationUnexpectedExit)
	budget := findIncident(t, repo, domain.ViolationRestartLimit)
	if source.RestartOutcome != domain.OutcomeStopped || budget.RestartOutcome != domain.OutcomeStopped {
		t.Fatalf("source=%+v budget=%+v", source, budget)
	}
	if len(controller.stops) != 2 || controller.stops[0] != controller.stops[1] {
		t.Fatalf("stop replay keys %v, want two identical calls", controller.stops)
	}
}
