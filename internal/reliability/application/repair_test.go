package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type repairFixture struct {
	rows             []RepairCompletedRow
	states           map[string]RepairTaskState
	readErrors       map[string]error
	completionErrors map[string]error
	handled          []RepairCompletedRow
	cleared          []string
}

func (f *repairFixture) ListRepairCandidates(context.Context, int) ([]RepairCandidate, error) {
	return nil, nil
}
func (f *repairFixture) RecordRepairSubmitted(context.Context, RepairCandidate, string) error {
	return nil
}
func (f *repairFixture) ListRepairCompleted(context.Context, int) ([]RepairCompletedRow, error) {
	return f.rows, nil
}
func (f *repairFixture) ClearRepairCompleted(_ context.Context, id string) error {
	f.cleared = append(f.cleared, id)
	return nil
}
func (f *repairFixture) SubmitRepair(context.Context, string, string, string, string, string, string) (string, string, error) {
	return "", "", nil
}
func (f *repairFixture) TaskState(_ context.Context, row RepairCompletedRow) (RepairTaskState, error) {
	return f.states[row.TaskID], f.readErrors[row.TaskID]
}
func (f *repairFixture) HandleRepairCompleted(_ context.Context, row RepairCompletedRow) error {
	f.handled = append(f.handled, row)
	return f.completionErrors[row.TaskID]
}
func TestRepairTerminalHandoffPreservesTaskAndContinuesAfterFailure(t *testing.T) {
	failure := errors.New("candidate not ready")
	f := &repairFixture{states: map[string]RepairTaskState{}, readErrors: map[string]error{}, completionErrors: map[string]error{"complete-without-candidate": failure}}
	for _, name := range []string{"pending", "failed", "complete-without-candidate", "complete", "read-failure", "unknown"} {
		f.rows = append(f.rows, RepairCompletedRow{RepairCandidate: RepairCandidate{IncidentID: name, OwnerUserID: "owner", ProjectID: "project", AppInstanceID: "installation"}, TaskID: name})
	}
	f.states["failed"] = RepairTaskFailed
	f.states["complete-without-candidate"], f.states["complete"] = RepairTaskCompleted, RepairTaskCompleted
	f.states["unknown"] = RepairTaskState(99)
	f.readErrors["read-failure"] = errors.New("Core unavailable")
	o, err := NewRepairOrchestrator(f, f, f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.RunPass(context.Background(), 4); err == nil {
		t.Fatal("pending errors hidden")
	}
	if !reflect.DeepEqual(f.cleared, []string{"failed", "complete"}) {
		t.Fatalf("cleared=%v", f.cleared)
	}
	if !reflect.DeepEqual(f.handled, []RepairCompletedRow{f.rows[2], f.rows[3]}) {
		t.Fatalf("handoff lost task identity or ran for unsuccessful task: %+v", f.handled)
	}
}
