package orchestration

import (
	"context"
	"errors"

	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	registryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectports "github.com/yangtao121/workos/internal/core/project/ports"
)

// CandidateTransitioner is the Project module's staged canary transition,
// adapted behind a neutral port so orchestration never imports transport.
type CandidateTransitioner interface {
	TransitionCandidate(ctx context.Context, input projectapp.TransitionInput) (projectports.InstallationResult, error)
	// ActiveFacts resolves the current pinned version and project revision
	// for the offer-time precondition snapshot (ADR-0026).
	ActiveFacts(ctx context.Context, ownerUserID, projectID, installationID string) (version string, revision int64, err error)
}

// RepairVersions coordinates the staged candidate lifecycle (ADR-0026):
// Core re-derives every fact from durable task, candidate and installation
// rows; Reliability only presents verified build verdicts.
type RepairVersions struct {
	pool        TaskTxSource
	sources     *RepairSources
	staging     *registryapp.StagingService
	transitions CandidateTransitioner
}

func NewRepairVersions(pool TaskTxSource, sources *RepairSources, staging *registryapp.StagingService, transitions CandidateTransitioner) (*RepairVersions, error) {
	if pool == nil || sources == nil || staging == nil {
		return nil, errors.New("repair versions require transactions, sources and staging")
	}
	return &RepairVersions{pool: pool, sources: sources, staging: staging, transitions: transitions}, nil
}

// Register verifies the completed repair task and creates the immutable
// staged candidate version. Same task replays exactly.
type RegisteredCandidate struct {
	registryapp.StagingResult
	// BaseVersion and ProjectRevision are the offer-time preconditions: the
	// installation pin observed when the staged version was registered.
	BaseVersion     string
	ProjectRevision int64
}

func (s *RepairVersions) Register(ctx context.Context, owner, taskID, projectID, installationID, buildJobID, sourceDigest string) (RegisteredCandidate, error) {
	if !agentdomain.ValidAppTaskUUID(owner) || !agentdomain.ValidAppTaskUUID(taskID) {
		return RegisteredCandidate{}, agentdomain.ErrInvalid
	}
	completed, err := s.sources.Completed(ctx, owner, taskID)
	if err != nil {
		return RegisteredCandidate{}, err
	}
	target := completed.Input.Target
	if completed.ProjectID != projectID || completed.IncidentID == "" || target.GetAppInstanceId() != installationID {
		return RegisteredCandidate{}, agentdomain.ErrInvalid
	}
	if completed.Candidate.Digest != sourceDigest {
		return RegisteredCandidate{}, agentdomain.ErrInvalid
	}
	if s.transitions == nil {
		return RegisteredCandidate{}, agentdomain.ErrInvalid
	}
	// The offer-time preconditions resolve from the durable installation
	// facts before any staged version exists: the current pin and revision.
	baseVersion, revision, err := s.transitions.ActiveFacts(ctx, owner, projectID, installationID)
	if err != nil {
		return RegisteredCandidate{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RegisteredCandidate{}, storeFailureContext("begin repair version registration", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	result, err := s.staging.Register(ctx, tx, registryapp.StagingRegistration{
		OwnerUserID: owner, TaskID: taskID, IncidentID: completed.IncidentID,
		ProjectID: projectID, InstallationID: installationID, BuildJobID: buildJobID,
		SourceDigest: sourceDigest, AppID: target.GetAppId(),
		BaseVersion: target.GetVersion(), BaseManifestDigest: target.GetManifestDigest(),
	})
	if err != nil {
		return RegisteredCandidate{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RegisteredCandidate{}, storeFailureContext("commit repair version registration", err)
	}
	return RegisteredCandidate{StagingResult: result, BaseVersion: baseVersion, ProjectRevision: revision}, nil
}

// Publish flips the staged version to published after the canary window.
func (s *RepairVersions) Publish(ctx context.Context, owner, taskID, projectID, installationID, version, manifestDigest string) (bool, error) {
	if !agentdomain.ValidAppTaskUUID(owner) || !agentdomain.ValidAppTaskUUID(taskID) {
		return false, agentdomain.ErrInvalid
	}
	if s.transitions == nil {
		return false, agentdomain.ErrInvalid
	}
	// ADR-0026: a user version change is never overridden by retries. The
	// publish precondition is that the installation still pins the staged
	// canary; anything else is a stable FailedPrecondition.
	pinned, _, err := s.transitions.ActiveFacts(ctx, owner, projectID, installationID)
	if err != nil {
		return false, err
	}
	if pinned != version {
		return false, ErrInstallationChanged
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, storeFailureContext("begin repair version publish", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	published, err := s.staging.Publish(ctx, tx, owner, taskID, version, manifestDigest)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, storeFailureContext("commit repair version publish", err)
	}
	return published, nil
}

// ErrInstallationChanged is the stable publish rejection when the user moved
// the installation off the canary pin (ADR-0026).
var ErrInstallationChanged = errors.New("installation no longer pins the staged candidate")

// TransitionCandidate pins the exact staged version for the canary window.
// The durable staged mapping must still match the installation facts; a user
// version change between registration and canary is a stable rejection.
func (s *RepairVersions) TransitionCandidate(ctx context.Context, owner, idempotencyKey, projectID, installationID, version, manifestDigest string, expectedRevision int64) (string, int64, error) {
	if s.transitions == nil {
		return "", 0, agentdomain.ErrInvalid
	}
	if !agentdomain.ValidAppTaskUUID(owner) || !agentdomain.ValidAppTaskUUID(installationID) {
		return "", 0, agentdomain.ErrInvalid
	}
	result, err := s.transitions.TransitionCandidate(ctx, projectapp.TransitionInput{
		OwnerUserID: owner, IdempotencyKey: idempotencyKey, ProjectID: projectID,
		InstallationID: installationID, Version: version, ManifestDigest: manifestDigest,
		ExpectedRevision: expectedRevision,
	})
	if err != nil {
		return "", 0, err
	}
	if result.Installation.Version != version {
		return "", 0, agentdomain.ErrInvalid
	}
	return result.Installation.Version, result.ProjectRevision, nil
}
