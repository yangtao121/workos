package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type deploymentMemory struct{ row *DeploymentRecord }

func (m *deploymentMemory) Start(_ context.Context, candidate DeploymentCandidate) error {
	if m.row == nil {
		m.row = &DeploymentRecord{DeploymentCandidate: candidate, State: DeploymentCandidateState}
	}
	return nil
}
func (m *deploymentMemory) Reconcile(_ context.Context, _ int, apply func(*DeploymentRecord) error) (int, error) {
	if m.row == nil || m.row.State == DeploymentPromoted || m.row.State == DeploymentRolledBack || m.row.State == DeploymentFailed {
		return 0, nil
	}
	return 1, apply(m.row)
}

type deploymentDriver struct {
	calls        []string
	failStart    bool
	failRollback bool
	revision     int64
}

func (d *deploymentDriver) Transition(_ context.Context, c DeploymentCandidate, key string) error {
	d.calls = append(d.calls, "pin:"+key)
	d.revision = c.ExpectedRevision
	return nil
}
func (d *deploymentDriver) StartSurface(_ context.Context, _ DeploymentCandidate, key string) error {
	d.calls = append(d.calls, "start:"+key)
	if d.failStart {
		return errors.New("runtime unavailable")
	}
	return nil
}
func (d *deploymentDriver) Rollback(_ context.Context, _ DeploymentCandidate, key string) error {
	d.calls = append(d.calls, "rollback:"+key)
	if d.failRollback {
		return errors.New("core unavailable")
	}
	return nil
}
func deploymentFixture(t *testing.T) (*DeploymentController, *deploymentMemory, *deploymentDriver) {
	t.Helper()
	id := func() string { return uuid.Must(uuid.NewV7()).String() }
	m, d := &deploymentMemory{}, &deploymentDriver{}
	c, err := NewDeploymentController(m, d, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Offer(context.Background(), DeploymentCandidate{IncidentID: id(), OwnerUserID: id(), ProjectID: id(), InstallationID: id(), TargetVersion: "2.0.0", ExpectedRevision: 7}); err != nil {
		t.Fatal(err)
	}
	return c, m, d
}
func passDeployment(t *testing.T, c *DeploymentController, now time.Time) {
	t.Helper()
	if _, err := c.Pass(context.Background(), now, 4); err != nil {
		t.Fatal(err)
	}
}
func TestDeploymentObservesOnlyAfterPinAndSurfaceStart(t *testing.T) {
	c, m, d := deploymentFixture(t)
	passDeployment(t, c, time.Now().Add(24*time.Hour))
	if m.row.State != DeploymentStarting || len(d.calls) != 1 || d.revision != 7 {
		t.Fatalf("pin must precede start: %+v %+v", m.row, d)
	}
	passDeployment(t, c, time.Now().Add(24*time.Hour))
	if m.row.State != DeploymentCanary || len(d.calls) != 2 || time.Until(m.row.CanaryUntil) < 59*time.Second {
		t.Fatalf("missing full observation window: %+v", m.row)
	}
	passDeployment(t, c, m.row.CanaryUntil.Add(-time.Second))
	if m.row.State != DeploymentCanary {
		t.Fatal("promoted early")
	}
	passDeployment(t, c, m.row.CanaryUntil)
	passDeployment(t, c, m.row.CanaryUntil.Add(time.Hour))
	if m.row.State != DeploymentPromoted || len(d.calls) != 2 {
		t.Fatal("promotion repeated side effects")
	}
}
func TestDeploymentRollsBackNewIncidentAndRetriesSameRequest(t *testing.T) {
	c, m, d := deploymentFixture(t)
	passDeployment(t, c, time.Now())
	passDeployment(t, c, time.Now())
	m.row.NewIncident = true
	passDeployment(t, c, m.row.CanaryUntil.Add(time.Hour))
	if m.row.State != DeploymentRollback {
		t.Fatal("incident must win over expiry")
	}
	d.failRollback = true
	passDeployment(t, c, time.Now())
	first := d.calls[len(d.calls)-1]
	d.failRollback = false
	// Recreate the controller to prove progress resides in the ledger.
	c, _ = NewDeploymentController(m, d, time.Minute)
	passDeployment(t, c, time.Now())
	if m.row.State != DeploymentRolledBack || d.calls[len(d.calls)-1] != first {
		t.Fatal("rollback replay changed identity")
	}
}
func TestDeploymentStartFailureRollsBackInsteadOfPromoting(t *testing.T) {
	c, m, d := deploymentFixture(t)
	d.failStart = true
	passDeployment(t, c, time.Now())
	for range 8 {
		passDeployment(t, c, time.Now())
	}
	if m.row.State != DeploymentRollback {
		t.Fatal("failed candidate start must roll back")
	}
	passDeployment(t, c, time.Now())
	if m.row.State != DeploymentRolledBack {
		t.Fatal("candidate was left pinned")
	}
}
func TestDeploymentRejectsTaskCompletionWithoutCandidate(t *testing.T) {
	m, d := &deploymentMemory{}, &deploymentDriver{}
	c, _ := NewDeploymentController(m, d, time.Minute)
	if !errors.Is(c.HandleRepairCompleted(context.Background(), RepairCandidate{}), ErrDeploymentCandidateRequired) {
		t.Fatal("completion alone became a deployment")
	}
	if !errors.Is(c.Offer(context.Background(), DeploymentCandidate{}), ErrDeploymentCandidateRequired) || m.row != nil || len(d.calls) != 0 {
		t.Fatal("invalid candidate had side effects")
	}
}
