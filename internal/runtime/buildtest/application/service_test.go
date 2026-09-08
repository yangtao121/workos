package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/buildtest/domain"
	"github.com/yangtao121/workos/internal/runtime/buildtest/ports"
)

type fakeStore struct {
	jobs     map[string]*domain.Job
	verdicts []string
	failNext error
}

func (f *fakeStore) InsertJob(_ context.Context, job domain.Job) (string, bool, error) {
	if f.failNext != nil {
		return "", false, f.failNext
	}
	if stored, ok := f.jobs[job.TaskID]; ok {
		return stored.InputDigest, false, nil
	}
	clone := job
	f.jobs[job.TaskID] = &clone
	return job.InputDigest, true, nil
}

func (f *fakeStore) GetJobByTask(_ context.Context, taskID string) (domain.Job, error) {
	if job, ok := f.jobs[taskID]; ok {
		return *job, nil
	}
	return domain.Job{}, domain.ErrNotFound
}

func (f *fakeStore) ListRunnable(_ context.Context, _ int, _ time.Time) ([]domain.Job, error) {
	jobs := make([]domain.Job, 0, len(f.jobs))
	for _, job := range f.jobs {
		if job.State == domain.StateQueued {
			jobs = append(jobs, *job)
		}
	}
	return jobs, nil
}

func (f *fakeStore) ClaimJob(_ context.Context, jobID, _ string, _, _ time.Time) (bool, error) {
	for _, job := range f.jobs {
		if job.ID == jobID && job.State == domain.StateQueued {
			job.State = domain.StateRunning
			job.Attempts++
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) RecordVerdict(_ context.Context, jobID, _ string, verdict domain.Job) error {
	for _, job := range f.jobs {
		if job.ID == jobID {
			job.State = verdict.State
			job.Stage = verdict.Stage
			job.FailureReason = verdict.FailureReason
			job.EngineFacts = verdict.EngineFacts
			job.LogTail = verdict.LogTail
			f.verdicts = append(f.verdicts, string(verdict.State))
			return nil
		}
	}
	return domain.ErrNotFound
}

func (f *fakeStore) RequeueTransient(_ context.Context, jobID, _ string, attempts, maxAttempts int32, stage domain.Stage) error {
	for _, job := range f.jobs {
		if job.ID == jobID {
			if attempts < maxAttempts {
				job.State = domain.StateQueued
				job.Stage = stage
				return nil
			}
			job.State = domain.StateFailed
			job.FailureReason = domain.FailureEngineFailed
			return nil
		}
	}
	return domain.ErrNotFound
}

func (f *fakeStore) CancelJob(_ context.Context, taskID string, _ time.Time) (domain.State, error) {
	if job, ok := f.jobs[taskID]; ok {
		if job.State.Terminal() {
			return job.State, nil
		}
		job.State = domain.StateCancelled
		return job.State, nil
	}
	return "", domain.ErrNotFound
}

type fakeEngine struct {
	facts  ports.EngineFacts
	result ports.RunResult
	err    error
}

func (f *fakeEngine) Facts() ports.EngineFacts        { return f.facts }
func (f *fakeEngine) Available(context.Context) error { return nil }
func (f *fakeEngine) Run(context.Context, ports.RunSpec) (ports.RunResult, error) {
	return f.result, f.err
}

type fixedIDs struct{}

func (fixedIDs) New() string {
	return "0198d7ea-2110-7c42-b659-c5e4d73bc341"
}

func validJob() domain.Job {
	return domain.Job{
		TaskID:         "0198d7ea-2110-7c42-b659-c5e4d73bc339",
		IncidentID:     "0198d7ea-2110-7c42-b659-c5e4d73bc340",
		OwnerUserID:    "0198d7ea-2110-7c42-b659-c5e4d73bc342",
		ProjectID:      "0198d7ea-2110-7c42-b659-c5e4d73bc343",
		InstallationID: "0198d7ea-2110-7c42-b659-c5e4d73bc344",
		SourceBundleID: "0198d7ea-2110-7c42-b659-c5e4d73bc345",
		InputDigest:    "sha256:" + repeat('a', 64),
		SourceDigest:   "sha256:" + repeat('b', 64),
		ManifestDigest: "sha256:" + repeat('c', 64),
		Payload: domain.Payload{
			BaseImage: "golang:1.26.7-bookworm",
			BuildCmd:  []string{"go", "build", "./..."},
			TestCmd:   []string{"go", "test", "./..."},
			Files:     []domain.File{{Path: "main.go", Content: []byte("package main\n")}},
		},
	}
}

func repeat(char rune, count int) string {
	out := make([]rune, count)
	for i := range out {
		out[i] = char
	}
	return string(out)
}

func newTestService(t *testing.T, engine ports.BuildEngine) (*Service, *fakeStore) {
	t.Helper()
	store := &fakeStore{jobs: map[string]*domain.Job{}}
	service, err := NewService(store, engine, fixedIDs{}, "runtime-test", time.Minute)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	return service, store
}

func TestSubmitIsIdempotentPerTask(t *testing.T) {
	service, store := newTestService(t, &fakeEngine{facts: ports.EngineFacts{Engine: "process"}})
	job := validJob()
	first, created, err := service.Submit(context.Background(), job)
	if err != nil || !created {
		t.Fatalf("first submit must create: %v %v", created, err)
	}
	replay, created, err := service.Submit(context.Background(), job)
	if err != nil || created || replay.InputDigest != first.InputDigest {
		t.Fatalf("replay must return the stored job: %+v %v %v", replay, created, err)
	}
	drifted := job
	drifted.Payload.BuildCmd = []string{"go", "build", "-race", "./..."}
	if _, _, err := service.Submit(context.Background(), drifted); !errors.Is(err, domain.ErrIdempotencyDrift) {
		t.Fatalf("different payload must abort, got %v", err)
	}
	if len(store.jobs) != 1 {
		t.Fatalf("exactly one job may exist, got %d", len(store.jobs))
	}
}

func TestRunPassDrivesSuccessVerdict(t *testing.T) {
	service, _ := newTestService(t, &fakeEngine{
		facts:  ports.EngineFacts{Engine: "process"},
		result: ports.RunResult{Stage: domain.StageVerify, Failure: domain.FailureNone},
	})
	job := validJob()
	if _, _, err := service.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	driven, err := service.RunPass(context.Background(), time.Now().UTC())
	if err != nil || driven != 1 {
		t.Fatalf("run pass: %d %v", driven, err)
	}
	stored, err := service.Get(context.Background(), job.TaskID)
	if err != nil || stored.State != domain.StateSucceeded {
		t.Fatalf("expected succeeded verdict, got %+v %v", stored, err)
	}
	if len(stored.EngineFacts) == 0 {
		t.Fatal("verdict must persist the engine facts")
	}
}

func TestRunPassFailsOnTestFailure(t *testing.T) {
	service, _ := newTestService(t, &fakeEngine{
		facts:  ports.EngineFacts{Engine: "process"},
		result: ports.RunResult{Stage: domain.StageTest, Failure: domain.FailureTestFailed},
	})
	job := validJob()
	if _, _, err := service.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunPass(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	stored, _ := service.Get(context.Background(), job.TaskID)
	if stored.State != domain.StateFailed || stored.FailureReason != domain.FailureTestFailed {
		t.Fatalf("failed build must never succeed: %+v", stored)
	}
}

func TestTransientEngineErrorsRequeueThenFail(t *testing.T) {
	service, store := newTestService(t, &fakeEngine{
		facts: ports.EngineFacts{Engine: "process"},
		err:   errors.New("engine hiccup"),
	})
	job := validJob()
	if _, _, err := service.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	// Two passes requeue within bounds; the third fails the job.
	for i := 0; i < 3; i++ {
		if _, err := service.RunPass(context.Background(), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if job := store.jobs[validJob().TaskID]; job.State != domain.StateFailed || job.FailureReason != domain.FailureEngineFailed {
		t.Fatalf("exhausted retries must fail the job: %+v", job)
	}
}

func TestCancelIsTerminalAndImmutable(t *testing.T) {
	service, _ := newTestService(t, &fakeEngine{facts: ports.EngineFacts{Engine: "process"}})
	job := validJob()
	if _, _, err := service.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	state, err := service.Cancel(context.Background(), job.TaskID)
	if err != nil || state != domain.StateCancelled {
		t.Fatalf("cancel: %v %v", state, err)
	}
	again, err := service.Cancel(context.Background(), job.TaskID)
	if err != nil || again != domain.StateCancelled {
		t.Fatalf("second cancel must replay the terminal state: %v %v", again, err)
	}
	// A cancelled job never runs.
	if driven, err := service.RunPass(context.Background(), time.Now().UTC()); err != nil || driven != 0 {
		t.Fatalf("cancelled job must not be driven: %d %v", driven, err)
	}
}
