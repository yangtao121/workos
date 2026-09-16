// Package application drives the durable Build/Test job state machine
// (ADR-0026 + ADR-0033): idempotent submit, bounded retries, crash recovery
// through leases, terminal verdicts that never manufacture a success, and
// the freeze of a verified build output into the release bundle repository
// before the success verdict is written.
package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	artifactapp "github.com/yangtao121/workos/internal/runtime/artifactstore/application"
	artifactdomain "github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
	"github.com/yangtao121/workos/internal/runtime/buildtest/domain"
	"github.com/yangtao121/workos/internal/runtime/buildtest/ports"
)

const (
	maxAttempts    = 3
	defaultTimeout = 10 * time.Minute
	defaultLease   = 10 * time.Minute
	maxBatch       = 8
)

var ErrEngineUnavailable = ports.ErrEngineUnavailable

// ArtifactCommitter freezes a verified build output directory into the
// release bundle repository; the artifactstore application service satisfies
// it directly, so the two state machines never drift apart.
type ArtifactCommitter interface {
	CommitBuild(ctx context.Context, commit artifactapp.BuildCommit) (artifactapp.Result, error)
}

type Service struct {
	store     ports.JobStore
	engine    ports.BuildEngine
	committer ArtifactCommitter
	generator ids.Generator
	identity  string
	scratch   string
	timeout   time.Duration
	lease     time.Duration
}

// NewService wires the job store, engine and optional bundle committer.
// scratch is the engine scratch root; the service creates one job-private
// directory beneath it per attempt and reclaims it after the verdict. An
// empty scratch keeps the service usable for stores that do not execute.
func NewService(store ports.JobStore, engine ports.BuildEngine, committer ArtifactCommitter, generator ids.Generator, identity, scratch string, timeout, lease time.Duration) (*Service, error) {
	if store == nil || engine == nil || generator == nil {
		return nil, errors.New("build test service requires store, engine and ids")
	}
	if strings.TrimSpace(identity) == "" {
		return nil, errors.New("build test service requires a lease identity")
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if lease <= 0 {
		lease = defaultLease
	}
	return &Service{store: store, engine: engine, committer: committer, generator: generator, identity: identity, scratch: scratch, timeout: timeout, lease: lease}, nil
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
		if stored.OwnerUserID != job.OwnerUserID || stored.ProjectID != job.ProjectID || stored.InstallationID != job.InstallationID ||
			stored.IncidentID != job.IncidentID || stored.SourceBundleID != job.SourceBundleID || stored.SourceDigest != job.SourceDigest || stored.ManifestDigest != job.ManifestDigest {
			return domain.Job{}, false, domain.ErrIdempotencyDrift
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
		claimTime := time.Now().UTC()
		token := s.identity + ":" + (ids.UUIDv7{}).New()
		leaseDuration := s.lease
		claimed, err := s.store.ClaimJob(ctx, job.ID, token, claimTime.Add(leaseDuration), claimTime)
		if err != nil {
			lastErr = err
			continue
		}
		if !claimed {
			continue
		}
		driven++
		attempt := *s
		attempt.identity = token
		attempt.drive(ctx, job)
	}
	return driven, lastErr
}

// drive executes one claimed job and persists its verdict. A verdict always
// terminates the attempt; transient engine errors requeue within bounds.
// The job-private scratch directory is reclaimed only after the verdict, so
// a committed bundle never depends on a deleted tree.
func (s *Service) drive(ctx context.Context, job domain.Job) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	jobDir := ""
	if s.scratch != "" {
		parent := filepath.Join(s.scratch, "jobs")
		if err := os.MkdirAll(parent, 0o700); err != nil {
			s.requeue(ctx, job, domain.StageMaterialize)
			return
		}
		var err error
		jobDir, err = os.MkdirTemp(parent, job.ID+"-")
		if err != nil {
			// No scratch, no execution: bounded requeue, never a fake verdict.
			s.requeue(ctx, job, domain.StageMaterialize)
			return
		}
		defer os.RemoveAll(jobDir)
	}
	spec := ports.RunSpec{
		BaseImage: job.Payload.BaseImage, BuildCommand: job.Payload.BuildCmd,
		TestCommand: job.Payload.TestCmd, Files: job.Payload.Files,
		OutputDirectory: job.Payload.OutputDirectory, RuntimeCommand: job.Payload.RuntimeCommand,
		Timeout: s.timeout, ScratchRoot: jobDir,
	}
	// A cancellation or lease takeover on any runtime process promptly stops
	// the container. The final SQL guard remains the authority for verdicts.
	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		ticker := time.NewTicker(max(time.Nanosecond, min(250*time.Millisecond, s.lease/3)))
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkCtx, stop := context.WithTimeout(ctx, time.Second)
				now := time.Now().UTC()
				renewed, err := s.store.RenewJobLease(checkCtx, job.ID, s.identity, now.Add(s.lease), now)
				stop()
				if err != nil || !renewed {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { close(watchDone); <-watchExited }()
	result, err := s.engine.Run(ctx, spec)
	verdict := job
	verdict.EngineFacts = ports.EngineFactsJSON(result.Facts)
	verdict.LogTail = result.LogTail
	if result.Stage != "" {
		verdict.Stage = result.Stage
	}
	switch {
	case err != nil:
		s.requeue(ctx, job, verdict.Stage)
		return
	case result.Failure != domain.FailureNone:
		verdict.State = domain.StateFailed
		verdict.FailureReason = result.Failure
		verdict.BuildExitCode = &result.BuildExitCode
		verdict.TestExitCode = &result.TestExitCode
	default:
		verdict.State = domain.StateSucceeded
		verdict.FailureReason = domain.FailureNone
		verdict.BuildExitCode = &result.BuildExitCode
		verdict.TestExitCode = &result.TestExitCode
		current, getErr := s.store.GetJobByTask(ctx, job.TaskID)
		if getErr != nil || current.State != domain.StateRunning || current.LeaseOwner != s.identity {
			return
		}
		if result.OutputDir != "" {
			// Build+test passed and the engine left a verified output tree:
			// freeze it as preparing before recording the success verdict.
			// Artifact facts promote it only after that exact verdict commits.
			if !s.commitOutput(ctx, job, result.OutputDir, &verdict) {
				return
			}
		}
	}
	if err := s.store.RecordVerdict(context.WithoutCancel(ctx), job.ID, s.identity, verdict); err != nil && !errors.Is(err, domain.ErrNotFound) {
		// Lease takeover or cancellation owns the authoritative verdict.
	}
}

// commitOutput freezes the verified build output and fills the verdict's
// artifact identity. It returns false when the service already requeued the
// attempt (transient repository failure); terminal freeze failures are
// recorded as output-failed verdicts by the caller.
func (s *Service) commitOutput(ctx context.Context, job domain.Job, outputDir string, verdict *domain.Job) bool {
	if s.committer == nil {
		// No repository wired: the engine produced a bundle this host cannot
		// freeze. Fail honestly instead of fabricating a bundleless success.
		verdict.State = domain.StateFailed
		verdict.FailureReason = domain.FailureOutputFailed
		return true
	}
	result, err := s.committer.CommitBuild(ctx, artifactapp.BuildCommit{
		AppID:           job.Payload.AppID,
		OwnerUserID:     job.OwnerUserID,
		TaskID:          job.TaskID,
		JobID:           job.ID,
		IncidentID:      job.IncidentID,
		ProjectID:       job.ProjectID,
		InstallationID:  job.InstallationID,
		SourceBundleID:  job.SourceBundleID,
		SourceDigest:    job.SourceDigest,
		ManifestDigest:  job.ManifestDigest,
		BaseImage:       job.Payload.BaseImage,
		BuildCommand:    job.Payload.BuildCmd,
		TestCommand:     job.Payload.TestCmd,
		OutputDirectory: job.Payload.OutputDirectory,
		IdempotencyKey:  "build-task:" + job.TaskID,
		OutputDir:       outputDir,
	})
	if err == nil {
		verdict.ArtifactID = result.Artifact.ID
		verdict.ArtifactDigest = result.Artifact.Digest
		return true
	}
	if errors.Is(err, artifactdomain.ErrStoreUnavailable) {
		// The repository is transiently down; the lease still guards the job.
		s.requeue(ctx, job, domain.StageVerify)
		return false
	}
	// Bundle invalid, oversized, quota exceeded, or same task already froze
	// different content: stable, terminal, never a success.
	verdict.State = domain.StateFailed
	verdict.FailureReason = domain.FailureOutputFailed
	return true
}

func (s *Service) requeue(ctx context.Context, job domain.Job, stage domain.Stage) {
	if requeueErr := s.store.RequeueTransient(context.WithoutCancel(ctx), job.ID, s.identity, job.Attempts+1, maxAttempts, stage); requeueErr != nil && !errors.Is(requeueErr, domain.ErrNotFound) {
		// The verdict could not be recorded; the lease expiry will retry.
	}
}

// Facts reports the engine identity for capability projections.
func (s *Service) Facts() ports.EngineFacts { return s.engine.Facts() }
