// Build coordination (ADR-0026): the repair-to-deployment hand-off that
// replaces the previous "task completion is not a candidate" refusal with the
// verified Build/Test chain. A deployable version still never appears from a
// task completion alone: Runtime's durable build verdict and Core's staged
// registration both have to succeed first.
package application

import (
	"context"
	"errors"
	"time"
)

// ErrBuildPending marks a submitted build whose terminal verdict has not
// arrived yet; the repair ledger row stays pollable.
var ErrBuildPending = errors.New("repair build verdict is pending")

// CandidateFacts is one completed repair task's immutable build input and
// candidate source as read from Core's private RepairCandidateService.
type CandidateFacts struct {
	TaskID         string
	IncidentID     string
	ProjectID      string
	OwnerUserID    string
	AppInstanceID  string
	AppID          string
	BaseVersion    string
	ManifestDigest string
	SourceBundleID string
	SourceDigest   string
	BaseImage      string
	BuildCommand   []string
	TestCommand    []string
	// Files carries the bounded candidate source (ADR-0024 limits).
	Files []CandidateFile
}

// CandidateFile is one regular candidate file.
type CandidateFile struct {
	Path       string
	Content    []byte
	Executable bool
}

// BuildTestVerdict is the bounded projection of one Runtime build job.
type BuildTestVerdict struct {
	JobID         string
	State         string
	Stage         string
	FailureReason string
	SourceDigest  string
}

// RegisteredVersion is Core's staged registration result.
type RegisteredVersion struct {
	Version         string
	ManifestDigest  string
	Created         bool
	BaseVersion     string
	ProjectRevision int64
}

// CandidateReader reads Core's private completed-task candidate facts.
type CandidateReader interface {
	Read(ctx context.Context, ownerUserID, taskID string) (CandidateFacts, error)
}

// BuildTestGateway drives Runtime's private Build/Test service.
type BuildTestGateway interface {
	Submit(ctx context.Context, facts CandidateFacts) (jobID string, created bool, err error)
	Get(ctx context.Context, taskID string) (BuildTestVerdict, error)
	Cancel(ctx context.Context, taskID string) error
}

// VersionRegistry drives Core's private staged candidate lifecycle.
type VersionRegistry interface {
	Register(ctx context.Context, ownerUserID, taskID, projectID, installationID, buildJobID, sourceDigest string) (RegisteredVersion, error)
	Publish(ctx context.Context, ownerUserID, taskID, projectID, installationID, version, manifestDigest string) (bool, error)
}

// BuildCoordinator advances one completed repair task through the verified
// build chain: candidate read → Runtime Build/Test → staged registration →
// deployment offer. Every step is idempotent; pending builds keep the ledger
// row; failed builds clear it without any deployment side effect.
type BuildCoordinator struct {
	candidates  CandidateReader
	builds      BuildTestGateway
	versions    VersionRegistry
	deployments *DeploymentController
	now         func() time.Time
}

func NewBuildCoordinator(candidates CandidateReader, builds BuildTestGateway, versions VersionRegistry, deployments *DeploymentController) (*BuildCoordinator, error) {
	if candidates == nil || builds == nil || versions == nil || deployments == nil {
		return nil, errors.New("build coordinator requires candidate reader, build gateway, version registry and deployments")
	}
	return &BuildCoordinator{candidates: candidates, builds: builds, versions: versions, deployments: deployments, now: func() time.Time { return time.Now().UTC() }}, nil
}

// HandleRepairCompleted drives the whole verified chain for one ledger row.
// It returns ErrBuildPending while Runtime has not produced a terminal
// verdict; the caller must keep the row. A failed build returns nil (the row
// clears, no deployment ever happens).
func (c *BuildCoordinator) HandleRepairCompleted(ctx context.Context, row RepairCompletedRow) error {
	facts, err := c.candidates.Read(ctx, row.OwnerUserID, row.TaskID)
	if err != nil {
		return err
	}
	if facts.IncidentID != row.IncidentID || facts.ProjectID != row.ProjectID || facts.AppInstanceID != row.AppInstanceID {
		return errors.New("repair candidate facts do not match the ledger row")
	}
	jobID, _, err := c.builds.Submit(ctx, facts)
	if err != nil {
		return err
	}
	verdict, err := c.builds.Get(ctx, row.TaskID)
	if err != nil {
		return err
	}
	switch verdict.State {
	case "queued", "running":
		return ErrBuildPending
	case "cancelled":
		return errors.New("repair build was cancelled")
	case "failed":
		// A failed build is terminal for this candidate: no version, no
		// deployment, no retry loop (a fresh repair task may re-run).
		return nil
	case "succeeded":
	default:
		return errors.New("repair build verdict is invalid")
	}
	if verdict.SourceDigest != facts.SourceDigest {
		return errors.New("repair build verdict source drifted")
	}
	registered, err := c.versions.Register(ctx, row.OwnerUserID, row.TaskID, row.ProjectID, row.AppInstanceID, jobID, facts.SourceDigest)
	if err != nil {
		return err
	}
	return c.deployments.Offer(ctx, DeploymentCandidate{
		IncidentID: row.IncidentID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
		InstallationID: row.AppInstanceID, TargetVersion: registered.Version,
		ExpectedRevision: registered.ProjectRevision,
		TaskID:           row.TaskID, ManifestDigest: registered.ManifestDigest,
	})
}
