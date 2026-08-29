package application

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/agent/ports"
)

// ApprovalService owns the owner-only pre-run approval facts. The App and the
// bridge surface can only observe the waiting task event; every decision
// path here is reached through the owner's identity alone.
type ApprovalService struct {
	repository    ports.Repository
	installations ports.InstallationSource
	providers     ports.ProviderCatalog
	now           func() time.Time
}

func NewApprovalService(repository ports.Repository, installations ports.InstallationSource, providers ports.ProviderCatalog) (*ApprovalService, error) {
	if repository == nil || installations == nil || providers == nil {
		return nil, errors.New("approval service requires repository, installation, and provider dependencies")
	}
	return &ApprovalService{repository: repository, installations: installations, providers: providers, now: func() time.Time { return time.Now().UTC() }}, nil
}

// List pages the owner's approvals in deterministic id order with optional
// project/state filters. Foreign or unknown projects simply yield no rows —
// the owner namespace bounds everything.
func (s *ApprovalService) List(ctx context.Context, ownerUserID, projectID string, state domain.ApprovalState, cursor string, limit int) ([]domain.Approval, error) {
	if ownerUserID == "" {
		return nil, domain.ErrInvalid
	}
	if projectID != "" && !domain.ValidAppTaskUUID(projectID) {
		return nil, domain.ErrInvalid
	}
	switch state {
	case domain.ApprovalState(""):
		state = ""
	case domain.ApprovalPending, domain.ApprovalApproved, domain.ApprovalRejected, domain.ApprovalExpired:
	default:
		return nil, domain.ErrInvalid
	}
	if cursor != "" && !domain.ValidAppTaskUUID(cursor) {
		return nil, domain.ErrInvalid
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	return s.repository.ListApprovals(ctx, ownerUserID, projectID, state, cursor, limit)
}

// Get reads one approval. Unknown or foreign approval IDs are the same
// sanitized NotFound — knowing an ID string grants nothing.
func (s *ApprovalService) Get(ctx context.Context, ownerUserID, approvalID string) (domain.Approval, error) {
	if ownerUserID == "" || !domain.ValidAppTaskUUID(approvalID) {
		return domain.Approval{}, domain.ErrInvalid
	}
	return s.repository.GetApproval(ctx, ownerUserID, approvalID)
}

// Decide adjudicates one owner decision. Before anything durable happens the
// service revalidates the world the approval was created in: installation
// liveness and grant membership, policy identity (the approval's policy
// revision must still be the effective one), and the provider's budget
// contract. Any revalidation failure is fail-closed FailedPrecondition
// semantics and keeps the approval pending; only the final repository
// transaction can move the approval, reserve quota, and enqueue the task
// atomically.
func (s *ApprovalService) Decide(ctx context.Context, input DecideInput) (domain.Approval, error) {
	if input.OwnerUserID == "" || !domain.ValidAppTaskUUID(input.ApprovalID) ||
		!domain.ValidAppClientIdempotencyKey(input.IdempotencyKey) {
		return domain.Approval{}, domain.ErrInvalid
	}
	switch input.Decision {
	case domain.ApprovalDecisionApprove, domain.ApprovalDecisionReject:
	default:
		return domain.Approval{}, domain.ErrInvalid
	}
	approval, err := s.repository.GetApproval(ctx, input.OwnerUserID, input.ApprovalID)
	if err != nil {
		return domain.Approval{}, err
	}
	if approval.State == domain.ApprovalExpired {
		return domain.Approval{}, domain.ErrApprovalNotPending
	}
	if approval.State != domain.ApprovalPending {
		// Already decided: the decision idempotency key replay/conflict
		// verdict is computed by the repository inside its transaction.
		return s.decide(ctx, input)
	}
	if err := s.revalidate(ctx, approval); err != nil {
		return domain.Approval{}, err
	}
	return s.decide(ctx, input)
}

// decide routes the command to the repository with the decision digest and
// the service clock filled in.
func (s *ApprovalService) decide(ctx context.Context, input DecideInput) (domain.Approval, error) {
	return s.repository.DecideApproval(ctx, ports.DecideApprovalCommand{
		OwnerUserID: input.OwnerUserID, ApprovalID: input.ApprovalID,
		Decision: input.Decision, IdempotencyKey: input.IdempotencyKey,
		DecisionDigest: domain.DecideApprovalRequestDigest(input.ApprovalID, input.Decision),
		Now:            s.now(),
	})
}

// revalidate walks the fail-closed chain: the installation must still be
// active under a non-archived project, its current grant must still carry
// agent.task.run, and the effective policy revision must still equal the
// revision the approval was created under (a real policy change has already
// expired the approval — any observed drift here is corruption-tier and
// treated as not-pending), and the provider must still declare the budget
// contract.
func (s *ApprovalService) revalidate(ctx context.Context, approval domain.Approval) error {
	facts, err := s.installations.ResolveActiveInstallation(ctx, approval.OwnerUserID, approval.ProjectID, approval.AppInstanceID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ErrApprovalNotPending
		}
		return err
	}
	granted := false
	for _, permission := range facts.GrantedPermissions {
		if permission == "agent.task.run" {
			granted = true
			break
		}
	}
	if !granted {
		return domain.ErrApprovalNotPending
	}
	policy, found, err := s.repository.GetPolicy(ctx, approval.OwnerUserID, approval.AppInstanceID)
	if err != nil {
		return err
	}
	effective := policy
	if !found {
		effective = domain.SystemDefaultPolicy()
	}
	if effective.Revision != approval.Revision {
		return domain.ErrApprovalNotPending
	}
	capabilities, err := s.providers.Capabilities(ctx, approval.ProviderID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ErrProviderCapabilityMissing
		}
		return err
	}
	if !capabilities.Complete() {
		return domain.ErrProviderCapabilityMissing
	}
	return nil
}

// DecideInput is one owner decision command.
type DecideInput struct {
	OwnerUserID    string
	ApprovalID     string
	Decision       domain.ApprovalDecision
	IdempotencyKey string
}
