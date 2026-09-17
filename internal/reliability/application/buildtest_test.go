package application

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type coordCandidates struct{ facts CandidateFacts }

func (c coordCandidates) Read(context.Context, string, string) (CandidateFacts, error) {
	return c.facts, nil
}

type coordBuilds struct{ verdict BuildTestVerdict }

func (c coordBuilds) Submit(context.Context, CandidateFacts) (string, bool, error) {
	return c.verdict.JobID, true, nil
}
func (c coordBuilds) Get(context.Context, string) (BuildTestVerdict, error) { return c.verdict, nil }
func (c coordBuilds) Cancel(context.Context, string) error                  { return nil }

type coordVersions struct {
	registered int
	err        error
}

func (c *coordVersions) Register(context.Context, string, string, string, string, string, string) (RegisteredVersion, error) {
	c.registered++
	if c.err != nil {
		return RegisteredVersion{}, c.err
	}
	return RegisteredVersion{Version: "1.0.1-repair.deadbeef", ManifestDigest: "sha256:" + strings.Repeat("d", 64), Created: true, BaseVersion: "1.0.0", ProjectRevision: 3}, nil
}
func (c *coordVersions) Publish(context.Context, string, string, string, string, string, string) (bool, error) {
	return true, nil
}

func TestHandleRepairCompletedRequiresReadyBundle(t *testing.T) {
	id := func() string { return uuid.Must(uuid.NewV7()).String() }
	row := RepairCompletedRow{TaskID: id(), RepairCandidate: RepairCandidate{
		IncidentID: id(), OwnerUserID: id(), ProjectID: id(), AppInstanceID: id(),
	}}
	facts := CandidateFacts{
		TaskID: row.TaskID, IncidentID: row.IncidentID, ProjectID: row.ProjectID,
		OwnerUserID: row.OwnerUserID, AppInstanceID: row.AppInstanceID, SourceDigest: "sha256:" + strings.Repeat("a", 64),
		OutputDirectory: "dist",
	}
	ledger := &deploymentMemory{}
	driver := &deploymentDriver{}
	deployments, err := NewDeploymentController(ledger, driver, 1)
	if err != nil {
		t.Fatal(err)
	}
	versions := &coordVersions{}
	coordinator, err := NewBuildCoordinator(coordCandidates{facts: facts}, coordBuilds{verdict: BuildTestVerdict{
		JobID: id(), State: "succeeded", SourceDigest: facts.SourceDigest,
	}}, versions, deployments)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.HandleRepairCompleted(context.Background(), row); err != nil {
		t.Fatalf("missing bundle must clear without error: %v", err)
	}
	if versions.registered != 0 || ledger.row != nil {
		t.Fatal("succeeded-without-ready-artifact must not register or offer")
	}
	coordinator, err = NewBuildCoordinator(coordCandidates{facts: facts}, coordBuilds{verdict: BuildTestVerdict{
		JobID: id(), State: "succeeded", SourceDigest: facts.SourceDigest,
		ArtifactID: id(), ArtifactDigest: "sha256:" + strings.Repeat("b", 64), ArtifactState: "ready", ArtifactFormat: "app-bundle.v1",
	}}, versions, deployments)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.HandleRepairCompleted(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if versions.registered != 1 || ledger.row == nil || ledger.row.ArtifactDigest != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("ready bundle must offer: registered=%d row=%+v", versions.registered, ledger.row)
	}
}

func TestCancelledRepairBuildCompletesWithoutDeployment(t *testing.T) {
	id := func() string { return uuid.Must(uuid.NewV7()).String() }
	row := RepairCompletedRow{TaskID: id(), RepairCandidate: RepairCandidate{
		IncidentID: id(), OwnerUserID: id(), ProjectID: id(), AppInstanceID: id(),
	}}
	facts := CandidateFacts{TaskID: row.TaskID, IncidentID: row.IncidentID,
		ProjectID: row.ProjectID, AppInstanceID: row.AppInstanceID}
	versions := &coordVersions{}
	deployments, err := NewDeploymentController(&deploymentMemory{}, &deploymentDriver{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewBuildCoordinator(coordCandidates{facts: facts},
		coordBuilds{verdict: BuildTestVerdict{JobID: id(), State: "cancelled"}}, versions, deployments)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.HandleRepairCompleted(context.Background(), row); err != nil {
		t.Fatalf("immutable cancellation must retire the repair poll: %v", err)
	}
	if versions.registered != 0 {
		t.Fatal("cancelled build registered a version")
	}
}

func TestHandleRepairCompletedImageOnlyDoesNotRequireArtifact(t *testing.T) {
	id := func() string { return uuid.Must(uuid.NewV7()).String() }
	row := RepairCompletedRow{TaskID: id(), RepairCandidate: RepairCandidate{
		IncidentID: id(), OwnerUserID: id(), ProjectID: id(), AppInstanceID: id(),
	}}
	facts := CandidateFacts{
		TaskID: row.TaskID, IncidentID: row.IncidentID, ProjectID: row.ProjectID,
		OwnerUserID: row.OwnerUserID, AppInstanceID: row.AppInstanceID, SourceDigest: "sha256:" + strings.Repeat("a", 64),
	}
	ledger := &deploymentMemory{}
	deployments, err := NewDeploymentController(ledger, &deploymentDriver{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	versions := &coordVersions{}
	coordinator, err := NewBuildCoordinator(coordCandidates{facts: facts}, coordBuilds{verdict: BuildTestVerdict{
		JobID: id(), State: "succeeded", SourceDigest: facts.SourceDigest,
	}}, versions, deployments)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.HandleRepairCompleted(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if versions.registered != 1 {
		t.Fatal("image-only success must still register")
	}
}

func TestSupersededRepairCompletesWithoutOffer(t *testing.T) {
	id := func() string { return uuid.Must(uuid.NewV7()).String() }
	row := RepairCompletedRow{TaskID: id(), RepairCandidate: RepairCandidate{IncidentID: id(), OwnerUserID: id(), ProjectID: id(), AppInstanceID: id()}}
	facts := CandidateFacts{TaskID: row.TaskID, IncidentID: row.IncidentID, ProjectID: row.ProjectID, AppInstanceID: row.AppInstanceID}
	ledger := &deploymentMemory{}
	deployments, err := NewDeploymentController(ledger, &deploymentDriver{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	versions := &coordVersions{err: ErrDeploymentSuperseded}
	coordinator, err := NewBuildCoordinator(coordCandidates{facts: facts}, coordBuilds{verdict: BuildTestVerdict{JobID: id(), State: "succeeded"}}, versions, deployments)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.HandleRepairCompleted(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if versions.registered != 1 || ledger.row != nil {
		t.Fatal("superseded repair must retire without deployment")
	}
}
