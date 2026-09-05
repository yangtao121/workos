// Deployment controller (ADR-0016 §6): after a repair task reaches a
// terminal state for an incident tied to an app installation, the
// controller runs a bounded canary window over the candidate version and
// then promotes — or rolls back to the previous pinned version when the
// canary sees a new incident for the same installation. Version facts stay
// in Core (ADR-0012): the controller drives the same public transition and
// rollback semantics over the loopback and never writes Core tables.
package application

import (
	"context"
	"fmt"
	"time"
)

// Deployment states.
const (
	DeploymentCanary     = "canary"
	DeploymentPromoted   = "promoted"
	DeploymentRolledBack = "rolled_back"
	DeploymentFailed     = "failed"
)

// DeploymentCandidate is one incident-scoped deployment to observe: the
// canary transitions the installation to the candidate target version, and
// a calm observation window promotes it.
type DeploymentCandidate struct {
	IncidentID     string
	OwnerUserID    string
	ProjectID      string
	InstallationID string
	TargetVersion  string
}

// DeploymentDriver drives Core's version transition and rollback semantics
// (ADR-0012) over the loopback. Implementations present the owner identity
// and persist nothing on the Core side.
type DeploymentDriver interface {
	// Transition performs the canary version switch to the explicit
	// candidate target version (ADR-0012 semantics). The error is
	// sanitized: transport-level failures surface as retryable.
	Transition(ctx context.Context, ownerUserID, projectID, installationID, targetVersion, idempotencyKey string) error
	// Rollback returns the installation to its previous pinned version.
	Rollback(ctx context.Context, ownerUserID, projectID, installationID, idempotencyKey string) error
}

// DeploymentLedger is the durable state machine storage.
type DeploymentLedger interface {
	// ListCanaryDue returns canary rows whose observation window has ended.
	ListCanaryDue(ctx context.Context, now time.Time, limit int) ([]DeploymentCandidate, error)
	// Start records a new canary row for one incident (idempotent per
	// incident) with the bounded observation deadline.
	Start(ctx context.Context, candidate DeploymentCandidate, canaryUntil time.Time) error
	// HasIncident checks whether the incident already carries a deployment.
	HasIncident(ctx context.Context, incidentID string) (bool, error)
	// SetState moves one row to a terminal state.
	SetState(ctx context.Context, incidentID, state string) error
}

// DeploymentController runs the bounded canary/promote-or-rollback state
// machine. L5 actions (data migrations, credentials, privilege escalation)
// are outside this controller by construction.
type DeploymentController struct {
	ledger       DeploymentLedger
	driver       DeploymentDriver
	canaryWindow time.Duration
}

func NewDeploymentController(ledger DeploymentLedger, driver DeploymentDriver, canaryWindow time.Duration) (*DeploymentController, error) {
	if ledger == nil || driver == nil {
		return nil, fmt.Errorf("deployment controller requires ledger and driver")
	}
	if canaryWindow <= 0 {
		return nil, fmt.Errorf("deployment controller requires a positive canary window")
	}
	return &DeploymentController{ledger: ledger, driver: driver, canaryWindow: canaryWindow}, nil
}

// Offer starts the canary for one incident-scoped candidate (idempotent per
// incident).
func (c *DeploymentController) Offer(ctx context.Context, candidate DeploymentCandidate) error {
	exists, err := c.ledger.HasIncident(ctx, candidate.IncidentID)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return c.ledger.Start(ctx, candidate, time.Now().UTC().Add(c.canaryWindow))
}

// HandleRepairCompleted implements the repair-to-deployment hand-off: the
// canary starts over the installation the incident came from, with the
// bounded observation window (ADR-0016 §5-6).
func (c *DeploymentController) HandleRepairCompleted(ctx context.Context, candidate RepairCandidate) error {
	return c.Offer(ctx, DeploymentCandidate{
		IncidentID:     candidate.IncidentID,
		OwnerUserID:    candidate.OwnerUserID,
		ProjectID:      candidate.ProjectID,
		InstallationID: candidate.AppInstanceID,
	})
}

// Pass reconciles the state machine once: due canaries whose observation
// window stayed calm get promoted. An empty target version means the repair
// ran on the pinned version — the calm window itself is the promotion.
func (c *DeploymentController) Pass(ctx context.Context, now time.Time, limit int) (int, error) {
	due, err := c.ledger.ListCanaryDue(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	promoted := 0
	for _, candidate := range due {
		if candidate.TargetVersion != "" {
			if err := c.driver.Transition(ctx, candidate.OwnerUserID, candidate.ProjectID, candidate.InstallationID, candidate.TargetVersion, "deploy-"+candidate.IncidentID); err != nil {
				if setErr := c.ledger.SetState(ctx, candidate.IncidentID, DeploymentFailed); setErr != nil {
					return promoted, setErr
				}
				continue
			}
		}
		if err := c.ledger.SetState(ctx, candidate.IncidentID, DeploymentPromoted); err != nil {
			return promoted, err
		}
		promoted++
	}
	return promoted, nil
}

// RollbackCanary rolls one canary back to the previous pinned version and
// marks the row terminal. Called when the canary observed a fresh incident.
func (c *DeploymentController) RollbackCanary(ctx context.Context, candidate DeploymentCandidate) error {
	if err := c.driver.Rollback(ctx, candidate.OwnerUserID, candidate.ProjectID, candidate.InstallationID, "rollback-"+candidate.IncidentID); err != nil {
		if setErr := c.ledger.SetState(ctx, candidate.IncidentID, DeploymentFailed); setErr != nil {
			return setErr
		}
		return err
	}
	return c.ledger.SetState(ctx, candidate.IncidentID, DeploymentRolledBack)
}
