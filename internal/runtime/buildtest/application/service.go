// Package application drives the durable Build/Test job state machine
// (ADR-0026): idempotent submit, bounded retries, crash recovery through
// leases, and terminal verdicts that never manufacture a success.
package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/buildtest/domain"
	"github.com/yangtao121/workos/internal/runtime/buildtest/ports"
)

const (
	maxAttempts    = 3
	leaseDuration  = 10 * time.Minute
	defaultTimeout = 10 * time.Minute
	maxBatch       = 8
)

var ErrEngineUnavailable = ports.ErrEngineUnavailable

type Service struct {
	store     ports.JobStore
	engine    ports.BuildEngine
	generator ids.Generator
	identity  string
	timeout   time.Duration
}

func NewService(store ports.JobStore, engine ports.BuildEngine, generator ids.Generator, identity string, timeout time.Duration) (*Service, error) {
	if store == nil || engine == nil || generator == nil {
		return nil, errors.New("build test service requires store, engine and ids")
	}
	if strings.TrimSpace(identity) == "" {
		return nil, errors.New("build test service requires a lease identity")
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Service{store: store, engine: engine, generator: generator, identity: identity, timeout: timeout}, nil
}

// Submit persists one queued job per task. Same task and same canonical
// payload replays exactly; a different payload is a stable conflict.
func (s *Service) Submit(ctx context.Context, job domain.Job) (domain.Job, bool, error) {
	if job.ID == "" {
		job.ID = s.generator.New()
	}
	if job.BaseImage == "" {
		job.BaseImage = job.Payload.BaseImage
	}
	job.State = domain.StateQueued
	job.Stage = domain.StageSubmit
	job.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	if err := domain.ValidateJob(job); err != nil {
		return domain.Job{}, false, err
	}
	_, digest, err := domain.CanonicalPayload(job.Payload)
	if err != nil || digest == "" {
		return domain.Job{}, false, domain.ErrInvalid
	}
	// The canonical payload digest is the replay authority — never the
	// caller-claimed input digest.
	job.InputDigest = digest
	storedDigest, created, err := s.store.InsertJob(ctx, job)
	if err != nil {
		return domain.Job{}, false, err
	}
	if !created && storedDigest != digest {
		return domain.Job{}, false, domain.ErrIdempotencyDrift
	}
	if !created {
		stored, err := s.store.GetJobByTask(ctx, job.TaskID)
		if err != nil {
			return domain.Job{}, false, err
		}
		return stored, false, nil
	}
	return job, true, nil
}

func (s *Service) Get(ctx context.Context, taskID string) (domain.Job, error) {
	if !domain.ValidUUIDv7(taskID) {
		return domain.Job{}, domain.ErrInvalid
	}
	return s.store.GetJobByTask(ctx, taskID)
}

// Cancel terminates a queued or running job; terminal verdicts are immutable.
func (s *Service) Cancel(ctx context.Context, taskID string) (domain.State, error) {
	if !domain.ValidUUIDv7(taskID) {
		return "", domain.ErrInvalid
	}
	return s.store.CancelJob(ctx, taskID, time.Now().UTC())
}

// Available reports whether the engine can accept submissions right now.
func (s *Service) Available(ctx context.Context) error { return s.engine.Available(ctx) }

// RunPass drives runnable jobs to terminal verdicts. Jobs run sequentially
// within the pass; the durable lease makes crashes safe and lets a restarted
// process finish what a dead one started.
func (s *Service) RunPass(ctx context.Context, now time.Time) (int, error) {
	jobs, err := s.store.ListRunnable(ctx, maxBatch, now)
	if err != nil {
		return 0, err
	}
	driven := 0
	var lastErr error
	for _, job := range jobs {
		if job.State == domain.StateRunning && job.ID == "" {
			continue
		}
		claimed, err := s.store.ClaimJob(ctx, job.ID, s.identity, now.Add(leaseDuration), now)
		if err != nil {
			lastErr = err
			continue
		}
		if !claimed {
			continue
		}
		driven++
		s.drive(ctx, job)
	}
	return driven, lastErr
}

// drive executes one claimed job and persists its verdict. A verdict always
// terminates the attempt; transient engine errors requeue within bounds.
func (s *Service) drive(ctx context.Context, job domain.Job) {
	spec := ports.RunSpec{
		BaseImage: job.Payload.BaseImage, BuildCommand: job.Payload.BuildCmd,
		TestCommand: job.Payload.TestCmd, Files: job.Payload.Files, Timeout: s.timeout,
	}
	result, err := s.engine.Run(ctx, spec)
	verdict := job
	verdict.EngineFacts = ports.EngineFactsJSON(result.Facts)
	verdict.LogTail = result.LogTail
	if result.Stage != "" {
		verdict.Stage = result.Stage
	}
	switch {
	case err != nil:
		// Transient engine failure: bounded requeue, terminal at exhaustion.
		if requeueErr := s.store.RequeueTransient(context.WithoutCancel(ctx), job.ID, s.identity, job.Attempts+1, maxAttempts, verdict.Stage); requeueErr != nil && !errors.Is(requeueErr, domain.ErrNotFound) {
			// The verdict could not be recorded; the lease expiry will retry.
		}
		return
	case result.Failure == domain.FailureNone:
		verdict.State = domain.StateSucceeded
		verdict.FailureReason = domain.FailureNone
	default:
		verdict.State = domain.StateFailed
		verdict.FailureReason = result.Failure
		verdict.BuildExitCode = &result.BuildExitCode
		verdict.TestExitCode = &result.TestExitCode
	}
	if err := s.store.RecordVerdict(context.WithoutCancel(ctx), job.ID, s.identity, verdict); err != nil && !errors.Is(err, domain.ErrNotFound) {
		// Lease takeover or cancellation owns the authoritative verdict.
	}
}
