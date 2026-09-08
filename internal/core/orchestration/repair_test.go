package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"google.golang.org/protobuf/encoding/protojson"
)

type repairAdmissionFixture struct {
	task           agentdomain.Task
	target         projectdomain.RepairTarget
	reads, submits int
	conflict       bool
}

func (f *repairAdmissionFixture) GetByIdempotency(context.Context, string, string) (agentdomain.Task, error) {
	if f.task.ID == "" {
		return agentdomain.Task{}, agentdomain.ErrNotFound
	}
	return f.task, nil
}
func (f *repairAdmissionFixture) Get(context.Context, string, string, string) (projectdomain.RepairTarget, error) {
	f.reads++
	return f.target, nil
}
func (f *repairAdmissionFixture) SubmitWithResult(_ context.Context, input agentapp.SubmitInput) (agentports.TaskSubmission, error) {
	f.submits++
	f.task = agentdomain.Task{ID: "task", OwnerUserID: input.OwnerUserID, ProjectID: input.ProjectID, Input: input.Payload, State: agentdomain.StateQueued}
	if f.conflict {
		payload := &agentv1.AgentTaskInput{}
		_ = protojson.Unmarshal(f.task.Input, payload)
		payload.RepairTarget.Version = "1.0.0"
		payload.RepairTarget.ProjectRevision = 2
		f.task.Input, _ = protojson.Marshal(payload)
		return agentports.TaskSubmission{}, agentdomain.ErrIdempotencyConflict
	}
	return agentports.TaskSubmission{Task: f.task, Created: true}, nil
}
func repairFixture(t *testing.T) (*RepairAdmission, *repairAdmissionFixture, RepairAdmissionInput) {
	t.Helper()
	input := RepairAdmissionInput{OwnerUserID: "01999999-9999-7999-8999-999999999991", ProjectID: "01999999-9999-7999-8999-999999999992", InstallationID: "01999999-9999-7999-8999-999999999993", IncidentID: "01999999-9999-7999-8999-999999999994", IdempotencyKey: "repair", Summary: "Repair startup failure"}
	f := &repairAdmissionFixture{target: projectdomain.RepairTarget{Installation: projectdomain.Installation{ID: input.InstallationID, OwnerUserID: input.OwnerUserID, ProjectID: input.ProjectID, AppID: "repair-app", Version: "2.0.0", ManifestDigest: "sha256:" + strings.Repeat("a", 64), GrantRevision: 1, InstalledAt: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)}, ProjectRevision: 3}}
	admission, err := NewRepairAdmission(f, f, f)
	if err != nil {
		t.Fatal(err)
	}
	return admission, f, input
}
func TestRepairAdmissionFreezesTargetAndReplaysQueuedTask(t *testing.T) {
	s, f, input := repairFixture(t)
	first, err := s.Submit(context.Background(), input)
	if err != nil || !first.Created {
		t.Fatalf("first admission: %+v %v", first, err)
	}
	payload := &agentv1.AgentTaskInput{}
	if err := protojson.Unmarshal(first.Input, payload); err != nil {
		t.Fatal(err)
	}
	target := payload.GetRepairTarget()
	if target.GetAppInstanceId() != input.InstallationID || target.GetAppId() != f.target.Installation.AppID || target.GetVersion() != "2.0.0" || target.GetManifestDigest() != f.target.Installation.ManifestDigest || target.GetProjectRevision() != 3 {
		t.Fatalf("incorrect snapshot: %v", target)
	}
	// A later archived/uninstalled or changed target must not be resolved on replay.
	f.target = projectdomain.RepairTarget{}
	replay, err := s.Submit(context.Background(), input)
	if err != nil || replay.Created || replay.ID != first.ID || string(replay.Input) != string(first.Input) || f.reads != 1 || f.submits != 1 {
		t.Fatalf("queued replay drifted: %+v %v reads=%d submits=%d", replay, err, f.reads, f.submits)
	}
	for _, field := range []string{"summary", "incident", "installation", "project", "owner"} {
		changed := input
		switch field {
		case "summary":
			changed.Summary += " changed"
		case "incident":
			changed.IncidentID = "01999999-9999-7999-8999-999999999995"
		case "installation":
			changed.InstallationID = changed.IncidentID
		case "project":
			changed.ProjectID = changed.IncidentID
		case "owner":
			changed.OwnerUserID = changed.IncidentID
		}
		if _, err := s.Submit(context.Background(), changed); !errors.Is(err, agentdomain.ErrIdempotencyConflict) {
			t.Fatalf("changed %s replayed: %v", field, err)
		}
	}
	if f.reads != 1 || f.submits != 1 {
		t.Fatal("conflicting replay produced effects")
	}
}
func TestRepairAdmissionAdoptsConcurrentFirstSnapshot(t *testing.T) {
	s, f, input := repairFixture(t)
	f.conflict = true
	result, err := s.Submit(context.Background(), input)
	payload := &agentv1.AgentTaskInput{}
	_ = protojson.Unmarshal(result.Input, payload)
	if err != nil || result.Created || payload.GetRepairTarget().GetVersion() != "1.0.0" || payload.GetRepairTarget().GetProjectRevision() != 2 {
		t.Fatalf("lost concurrent winner: %v %v", payload, err)
	}
}
func TestRepairAdmissionRejectsInvalidTargets(t *testing.T) {
	for _, field := range []string{"owner", "project", "installation", "inactive", "revision", "digest"} {
		t.Run(field, func(t *testing.T) {
			s, f, input := repairFixture(t)
			switch field {
			case "owner":
				f.target.Installation.OwnerUserID = input.IncidentID
			case "project":
				f.target.Installation.ProjectID = input.IncidentID
			case "installation":
				f.target.Installation.ID = input.IncidentID
			case "inactive":
				at := f.target.Installation.InstalledAt
				f.target.Installation.UninstalledAt = &at
			case "revision":
				f.target.ProjectRevision = 0
			case "digest":
				f.target.Installation.ManifestDigest = "bad"
			}
			if _, err := s.Submit(context.Background(), input); !errors.Is(err, projectdomain.ErrInstallationCorrupt) || f.submits != 0 {
				t.Fatalf("invalid target submitted: %v", err)
			}
		})
	}
}
func TestRepairAdmissionRejectsUnboundHistoricalTask(t *testing.T) {
	s, f, input := repairFixture(t)
	f.task = agentdomain.Task{ID: "old", OwnerUserID: input.OwnerUserID, ProjectID: input.ProjectID}
	f.task.Input, _ = protojson.Marshal(repairTaskInput(input, nil))
	if _, err := s.Submit(context.Background(), input); !errors.Is(err, agentdomain.ErrIdempotencyConflict) || f.reads != 0 || f.submits != 0 {
		t.Fatalf("historical target guessed: %v", err)
	}
}
