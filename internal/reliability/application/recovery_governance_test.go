package application

import (
	"context"
	"errors"
	"testing"
)

// awaitingFixture drives the pass with a submitter whose admission ends in
// the both-tiers-unavailable verdict.
type awaitingFixture struct {
	repairFixture
	candidates  []RepairCandidate
	submitErr   error
	awaiting    []string
	submittedTo []string
}

func (f *awaitingFixture) ListRepairCandidates(context.Context, int) ([]RepairCandidate, error) {
	return f.candidates, nil
}

func (f *awaitingFixture) RecordRepairAwaitingManual(_ context.Context, incidentID string) error {
	f.awaiting = append(f.awaiting, incidentID)
	return nil
}

func (f *awaitingFixture) SubmitRepair(_ context.Context, _, _, _, _, incidentID, _ string) (string, string, error) {
	f.submittedTo = append(f.submittedTo, incidentID)
	if f.submitErr != nil {
		return "", "", f.submitErr
	}
	return "task-" + incidentID, "recovery-cli", nil
}

// The awaiting-manual verdict terminates the incident's retry loop: the
// ledger row records it and the pass neither errors nor blocks later work.
func TestRepairPassRecordsAwaitingManual(t *testing.T) {
	f := &awaitingFixture{submitErr: ErrRepairAwaitingManual}
	f.candidates = []RepairCandidate{{IncidentID: "incident-manual", OwnerUserID: "owner", ProjectID: "project", AppInstanceID: "installation"}}
	o, err := NewRepairOrchestrator(f, f, f)
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := o.RunPass(context.Background(), 4)
	if err != nil {
		t.Fatalf("awaiting manual must not surface as a pass error: %v", err)
	}
	if submitted != 0 {
		t.Fatal("awaiting manual must not count as a submitted task")
	}
	if len(f.awaiting) != 1 || f.awaiting[0] != "incident-manual" {
		t.Fatalf("ledger must record awaiting_manual once: %v", f.awaiting)
	}
}

// Transient admission failures (anything but the awaiting-manual verdict)
// still retry on the next pass without touching the ledger state.
func TestRepairPassRetriesTransientAdmissionFailures(t *testing.T) {
	f := &awaitingFixture{submitErr: errors.New("core restarting")}
	f.candidates = []RepairCandidate{{IncidentID: "incident-transient", OwnerUserID: "owner", ProjectID: "project", AppInstanceID: "installation"}}
	o, err := NewRepairOrchestrator(f, f, f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.RunPass(context.Background(), 4); err == nil {
		t.Fatal("transient admission failure must surface for the log")
	}
	if len(f.awaiting) != 0 {
		t.Fatalf("transient failure must not mark awaiting_manual: %v", f.awaiting)
	}
}
