// Package ports defines the neutral Build/Test engine and job-store
// boundaries (ADR-0026). Engines execute the trusted fixed argv only; the
// store owns durable job facts.
package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/runtime/buildtest/domain"
)

// EngineFacts are the isolation facts an engine actually enforced for a run.
// The reduced process tier reports its own limits honestly; nothing here is
// asserted from configuration alone.
type EngineFacts struct {
	Engine          string   `json:"engine"`
	NetworkIsolated bool     `json:"network_isolated"`
	ImagePinned     bool     `json:"image_pinned"`
	EnforcedLimits  []string `json:"enforced_limits"`
}

// RunSpec is the complete, server-owned execution request.
type RunSpec struct {
	ScratchRoot  string
	BaseImage    string
	BuildCommand []string
	TestCommand  []string
	Files        []domain.File
	Timeout      time.Duration
}

// RunResult is the deterministic verdict of one engine run. A non-nil
// RunResult always terminates the job; an error marks a transient engine
// failure the caller may retry within its bounded attempts.
type RunResult struct {
	Stage         domain.Stage
	BuildExitCode int32
	TestExitCode  int32
	Failure       domain.FailureReason
	LogTail       string
	Facts         EngineFacts
}

// ErrEngineUnavailable marks an engine that cannot execute at all (missing
// toolchain, unusable scratch root): submit must fail closed.
var ErrEngineUnavailable = errors.New("build engine is unavailable")

// BuildEngine executes one bounded build/test run from trusted inputs. It
// must never execute candidate-provided container/network/mount config, never
// choose its own commands, and never pull images implicitly.
type BuildEngine interface {
	// Facts reports the engine identity and enforced isolation facts.
	Facts() EngineFacts
	// Available verifies the engine can run (toolchain and scratch root).
	Available(ctx context.Context) error
	// Run materializes the files and executes build then test.
	Run(ctx context.Context, spec RunSpec) (RunResult, error)
}

// JobStore owns the durable build job ledger.
type JobStore interface {
	// InsertJob persists a new queued job; a same-task replay returns the
	// stored input digest for drift adjudication (created=false).
	InsertJob(ctx context.Context, job domain.Job) (storedInputDigest string, created bool, err error)
	GetJobByTask(ctx context.Context, taskID string) (domain.Job, error)
	// ListRunnable returns queued jobs plus running jobs whose lease
	// expired, oldest update first, bounded.
	ListRunnable(ctx context.Context, limit int, now time.Time) ([]domain.Job, error)
	// ClaimJob transitions one job to running under an exclusive lease.
	ClaimJob(ctx context.Context, jobID, leaseOwner string, leaseUntil time.Time, now time.Time) (bool, error)
	// RecordVerdict persists the terminal verdict of the lease holder.
	RecordVerdict(ctx context.Context, jobID, leaseOwner string, verdict domain.Job) error
	// RequeueTransient returns a lease holder's job to queued for one more
	// bounded attempt, or fails it as engine-failed at exhaustion.
	RequeueTransient(ctx context.Context, jobID, leaseOwner string, attempts int32, maxAttempts int32, stage domain.Stage) error
	// CancelJob terminates a queued or running job.
	CancelJob(ctx context.Context, taskID string, now time.Time) (domain.State, error)
}

// EngineFactsJSON is the durable projection of the engine facts.
func EngineFactsJSON(facts EngineFacts) json.RawMessage {
	encoded, err := json.Marshal(facts)
	if err != nil {
		return json.RawMessage(`{"engine":"unknown"}`)
	}
	return encoded
}
