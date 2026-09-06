package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	DeploymentCandidateState = "candidate"
	DeploymentStarting       = "starting"
	DeploymentRollback       = "rollback"
	DeploymentCanary         = "canary"
	DeploymentPromoted       = "promoted"
	DeploymentRolledBack     = "rolled_back"
	DeploymentFailed         = "failed"
)

var ErrDeploymentCandidateRequired = errors.New("deployment requires a verified candidate version and project revision")

// A candidate refers to an immutable registered version. The revision is
// captured before any side effect and reused unchanged on every replay.
type DeploymentCandidate struct {
	IncidentID       string
	OwnerUserID      string
	ProjectID        string
	InstallationID   string
	TargetVersion    string
	ExpectedRevision int64
}

type DeploymentRecord struct {
	DeploymentCandidate
	State           string
	Attempts        int32
	CanaryStartedAt time.Time
	CanaryUntil     time.Time
	NewIncident     bool
}

type DeploymentDriver interface {
	Transition(context.Context, DeploymentCandidate, string) error
	Rollback(context.Context, DeploymentCandidate, string) error
	StartSurface(context.Context, DeploymentCandidate, string) error
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
	return c.ledger.Start(ctx, candidate)
}

// Task completion alone is not a deployment candidate. Admission of a
// verified version must happen separately; never manufacture a promotion.
func (c *DeploymentController) HandleRepairCompleted(context.Context, RepairCandidate) error {
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
			row.Attempts++
			if err := c.driver.StartSurface(ctx, row.DeploymentCandidate, "deploy-"+row.IncidentID+"-surface"); err != nil {
				if row.Attempts >= 8 {
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
			if row.NewIncident {
				row.State = DeploymentRollback
			} else if !now.Before(row.CanaryUntil) {
				row.State = DeploymentPromoted
			}
		case DeploymentRollback:
			row.Attempts++
			if err := c.driver.Rollback(ctx, row.DeploymentCandidate, "rollback-"+row.IncidentID); err != nil {
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
