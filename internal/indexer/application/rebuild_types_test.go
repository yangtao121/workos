package application

import (
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/indexer/domain"
)

func TestRebuildRequestValidation(t *testing.T) {
	t.Parallel()
	valid := RebuildRequest{Scope: "project", OwnerUserID: "01999999-9999-7999-8999-000000000021", ProjectID: "01999999-9999-7999-8999-000000000022", IdempotencyKey: "rebuild-project-1"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RebuildRequest){
		"36-char non uuid":   func(r *RebuildRequest) { r.ProjectID = "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" },
		"uuid v4":            func(r *RebuildRequest) { r.ProjectID = "550e8400-e29b-41d4-a716-446655440000" },
		"control key":        func(r *RebuildRequest) { r.IdempotencyKey = "bad\nkey" },
		"all with scope ids": func(r *RebuildRequest) { r.Scope = RebuildScopeAll },
	} {
		request := valid
		mutate(&request)
		if err := request.Validate(); err != ErrInvalidRebuild {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

func TestValidateStoredRebuildJobRejectsCorruption(t *testing.T) {
	t.Parallel()
	now := time.Unix(1, 0).UTC()
	valid := RebuildJobView{
		ID: "01999999-9999-7999-8999-000000000023", Scope: RebuildScopeAll,
		State: "snapshotting", TargetGeneration: "01999999-9999-7999-8999-000000000024",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := ValidateStoredRebuildJob(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RebuildJobView){
		"unknown state": func(job *RebuildJobView) { job.State = "mystery" },
		"v4 id":         func(job *RebuildJobView) { job.ID = "550e8400-e29b-41d4-a716-446655440000" },
		"count order":   func(job *RebuildJobView) { job.SourceCount, job.AppliedCount = 2, 1 },
		"terminal time": func(job *RebuildJobView) { job.State = "completed" },
	} {
		job := valid
		mutate(&job)
		if err := ValidateStoredRebuildJob(job); err != domain.ErrCorrupt {
			t.Fatalf("%s: error=%v", name, err)
		}
	}
}
