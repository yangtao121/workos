package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yangtao121/workos/internal/platform/faultinject"
)

const (
	DeploymentCandidateState  = "candidate"
	DeploymentStarting        = "starting"
	DeploymentRollback        = "rollback"
	DeploymentCanary          = "canary"
	DeploymentPromoted        = "promoted"
	DeploymentRolledBack      = "rolled_back"
	DeploymentRollbackPending = "rollback_pending"
	DeploymentFailed          = "failed"
	DeploymentSuperseded      = "superseded"
)

var ErrDeploymentCandidateRequired = errors.New("deployment requires a verified candidate version and project revision")

// ErrDeploymentSuperseded is the stable canary refusal when the user moved
// the installation off the candidate pin. Automation never overrides that pin.
var ErrDeploymentSuperseded = errors.New("installation pin moved off the candidate")

// ErrDeploymentActive defers an offer while a different incident's
// deployment is still in flight for the same installation (ADR-0026: one
// canary per installation; concurrent incidents serialize).
var ErrDeploymentActive = errors.New("installation already has an active deployment")

// A candidate refers to an immutable registered version. The revision is
// captured before any side effect and reused unchanged on every replay.
// Since ADR-0026 a repair candidate additionally carries the staged facts:
// the producing task, the staged manifest digest and the version the
// installation pinned when the staged version was registered (the canary
// precondition — a user version change in between is a stable rejection,
// never overridden by retries).
type DeploymentCandidate struct {
	IncidentID         string
	OwnerUserID        string
	ProjectID          string
	InstallationID     string
	TargetVersion      string
	ExpectedRevision   int64
	TaskID             string
	ManifestDigest     string
	BaseVersion        string
	ArtifactID         string
	ArtifactDigest     string
	BaseArtifactDigest string
	WorkloadID         string
	WorkloadGeneration int64
}

// Staged reports whether this candidate went through the verified
// Build/Test chain (ADR-0026).
func (c DeploymentCandidate) Staged() bool {
	return c.TaskID != "" && c.ManifestDigest != ""
}

type DeploymentRecord struct {
	DeploymentCandidate
	UpdatedAt       time.Time
	State           string
	Attempts        int32
	CanaryStartedAt time.Time
	CanaryUntil     time.Time
	NewIncident     bool
}

type DeploymentDriver interface {
	Verify(context.Context, *DeploymentCandidate) error
	Transition(context.Context, DeploymentCandidate, string) error
	Rollback(context.Context, DeploymentCandidate, string) error
	StartSurface(context.Context, DeploymentCandidate, string) error
	// Publish flips the staged candidate to a published version after the
	// canary window passed (ADR-0026). It is a durable no-op on replay.
	Publish(context.Context, DeploymentCandidate) error
}

type DeploymentLedger interface {
	Start(context.Context, DeploymentCandidate) error
	// Reconcile serializes each row with a database lock, persists the
	// callback's state atomically, and never selects terminal rows.
	Reconcile(context.Context, int, func(*DeploymentRecord) error) (int, error)
}

type DeploymentController struct {
	ledger       DeploymentLedger
	driver       DeploymentDriver
	canaryWindow time.Duration
}

func NewDeploymentController(ledger DeploymentLedger, driver DeploymentDriver, canaryWindow time.Duration) (*DeploymentController, error) {
	if ledger == nil || driver == nil || canaryWindow <= 0 {
		return nil, errors.New("deployment requires ledger, driver and positive observation window")
	}
	return &DeploymentController{ledger: ledger, driver: driver, canaryWindow: canaryWindow}, nil
}

func (c *DeploymentController) Offer(ctx context.Context, candidate DeploymentCandidate) error {
	for _, id := range []string{candidate.IncidentID, candidate.OwnerUserID, candidate.ProjectID, candidate.InstallationID} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 || parsed.String() != id {
			return ErrDeploymentCandidateRequired
		}
	}
	if candidate.ExpectedRevision <= 0 || strings.TrimSpace(candidate.TargetVersion) == "" || len(candidate.TargetVersion) > 64 {
		return ErrDeploymentCandidateRequired
	}
	if candidate.Staged() {
		parsed, err := uuid.Parse(candidate.TaskID)
		if err != nil || parsed.Version() != 7 || parsed.String() != candidate.TaskID {
			return ErrDeploymentCandidateRequired
		}
		if len(candidate.ManifestDigest) != 71 || !strings.HasPrefix(candidate.ManifestDigest, "sha256:") {
			return ErrDeploymentCandidateRequired
		}
	}
	err := c.ledger.Start(ctx, candidate)
	if err == nil {
		faultinject.Arrive(ctx, "deployment-offer")
	}
	return err
}

// Task completion alone is not a deployment candidate. Admission of a
// verified version must happen separately; never manufacture a promotion.
func (c *DeploymentController) HandleRepairCompleted(context.Context, RepairCompletedRow) error {
	return ErrDeploymentCandidateRequired
}

func (c *DeploymentController) Pass(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, errors.New("invalid deployment batch size")
	}
	return c.ledger.Reconcile(ctx, limit, func(row *DeploymentRecord) error {
		switch row.State {
		case DeploymentCandidateState:
			row.Attempts++
			if err := c.driver.Transition(ctx, row.DeploymentCandidate, "deploy-"+row.IncidentID); err != nil {
				if row.Attempts >= 8 {
					row.State = DeploymentFailed
				}
				return nil
			}
			row.State = DeploymentStarting
			row.Attempts = 0
		case DeploymentStarting:
			if row.NewIncident {
				row.State = DeploymentRollback
				row.Attempts = 0
				return nil
			}
			row.Attempts++
			if err := c.driver.StartSurface(ctx, row.DeploymentCandidate, "deploy-"+row.IncidentID+"-surface"); err != nil {
				if row.Attempts >= 8 {
					row.State = DeploymentRollback
					row.Attempts = 0
				}
				return nil
			}
			if err := c.driver.Verify(ctx, &row.DeploymentCandidate); err != nil {
				if errors.Is(err, ErrDeploymentSuperseded) {
					row.State = DeploymentSuperseded
				} else if row.Attempts >= 8 {
					row.State = DeploymentRollback
					row.Attempts = 0
				}
				return nil
			}
			row.State = DeploymentCanary
			row.Attempts = 0
			row.CanaryStartedAt = time.Now().UTC()
			row.CanaryUntil = row.CanaryStartedAt.Add(c.canaryWindow)
		case DeploymentCanary:
			faultinject.Arrive(ctx, "deployment-canary")
			if err := c.driver.Verify(ctx, &row.DeploymentCandidate); err != nil {
				row.Attempts = 0
				if errors.Is(err, ErrDeploymentSuperseded) {
					row.State = DeploymentSuperseded
				} else {
					row.State = DeploymentRollback
				}
				return nil
			}
			if row.NewIncident {
				row.State = DeploymentRollback
				row.Attempts = 0
				return nil
			} else if !now.Before(row.CanaryUntil) {
				row.Attempts++
				if err := c.driver.Publish(ctx, row.DeploymentCandidate); err != nil {
					if errors.Is(err, ErrDeploymentSuperseded) {
						row.State = DeploymentSuperseded
						return nil
					}
					if row.Attempts >= 8 {
						row.State = DeploymentFailed
					}
					return nil
				}
				row.State = DeploymentPromoted
			}
		case DeploymentRollback, DeploymentRollbackPending:
			row.Attempts++
			if err := c.driver.Rollback(ctx, row.DeploymentCandidate, "rollback-"+row.IncidentID); err != nil {
				row.State = DeploymentRollbackPending
				if row.Attempts >= 8 {
					row.State = DeploymentFailed
				}
				return nil
			}
			row.State = DeploymentRolledBack
		}
		return nil
	})
}
