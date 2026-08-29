package application

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/agent/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// PolicyService owns the Agent-side policy facts: effective policy reads
// (explicit row or versioned system default) and the full-replacement Set
// command. Every mutation revalidates installation liveness through the
// neutral InstallationSource port — never through Project tables or adapters.
type PolicyService struct {
	repository    ports.Repository
	installations ports.InstallationSource
	ids           ids.Generator
	now           func() time.Time
}

func NewPolicyService(repository ports.Repository, installations ports.InstallationSource, generator ids.Generator) (*PolicyService, error) {
	if repository == nil || installations == nil || generator == nil {
		return nil, errors.New("policy service requires repository, installation, and id dependencies")
	}
	return &PolicyService{repository: repository, installations: installations, ids: generator, now: func() time.Time { return time.Now().UTC() }}, nil
}

// EffectivePolicy resolves the policy governing fresh App runs for one
// installation: the explicit owner-set row, or the versioned system default.
// Liveness of the (owner, project, installation) triple is revalidated first;
// unknown, foreign, uninstalled, or archived-project facts are a sanitized
// NotFound.
func (s *PolicyService) EffectivePolicy(ctx context.Context, ownerUserID, projectID, appInstanceID string) (domain.Policy, error) {
	if ownerUserID == "" || !domain.ValidAppTaskUUID(projectID) || !domain.ValidAppTaskUUID(appInstanceID) {
		return domain.Policy{}, domain.ErrInvalid
	}
	facts, err := s.installations.ResolveActiveInstallation(ctx, ownerUserID, projectID, appInstanceID)
	if err != nil {
		return domain.Policy{}, err
	}
	policy, found, err := s.repository.GetPolicy(ctx, ownerUserID, appInstanceID)
	if err != nil {
		return domain.Policy{}, err
	}
	if !found {
		policy = domain.SystemDefaultPolicy()
	}
	policy.OwnerUserID = ownerUserID
	policy.AppInstanceID = appInstanceID
	policy.ProjectID = projectID
	policy.AppID = facts.AppID
	return policy, nil
}

// SetPolicy applies one full-replacement policy mutation. The canonical
// request digest covers only the client-visible request (project, install-
// ation, expected revision, spec); liveness is revalidated before any key is
// consumed; failures never consume the key.
func (s *PolicyService) SetPolicy(ctx context.Context, input SetPolicyInput) (domain.Policy, ports.SetPolicyResult, error) {
	if input.OwnerUserID == "" || !domain.ValidAppTaskUUID(input.ProjectID) || !domain.ValidAppTaskUUID(input.AppInstanceID) ||
		!domain.ValidAppClientIdempotencyKey(input.IdempotencyKey) ||
		input.ExpectedPolicyRevision < 0 {
		return domain.Policy{}, ports.SetPolicyResult{}, domain.ErrInvalid
	}
	if err := input.Spec.Validate(); err != nil {
		return domain.Policy{}, ports.SetPolicyResult{}, err
	}
	if _, err := s.installations.ResolveActiveInstallation(ctx, input.OwnerUserID, input.ProjectID, input.AppInstanceID); err != nil {
		return domain.Policy{}, ports.SetPolicyResult{}, err
	}
	digest := domain.SetPolicyRequestDigest(input.ProjectID, input.AppInstanceID, input.ExpectedPolicyRevision, input.Spec)
	policy, result, err := s.repository.SetPolicy(ctx, ports.SetPolicyCommand{
		OwnerUserID:            input.OwnerUserID,
		AppInstanceID:          input.AppInstanceID,
		ProjectID:              input.ProjectID,
		Spec:                   input.Spec,
		SpecDigest:             input.Spec.Digest(),
		ExpectedPolicyRevision: input.ExpectedPolicyRevision,
		IdempotencyKey:         input.IdempotencyKey,
		RequestDigest:          digest,
		Now:                    s.now(),
	})
	if err != nil {
		return domain.Policy{}, ports.SetPolicyResult{}, err
	}
	policy.OwnerUserID = input.OwnerUserID
	policy.AppInstanceID = input.AppInstanceID
	if policy.ProjectID == "" {
		policy.ProjectID = input.ProjectID
	}
	return policy, result, nil
}

// GetPolicyRequest exposes the consumed-key read for replay adjudication.
func (s *PolicyService) GetPolicyRequest(ctx context.Context, ownerUserID, idempotencyKey string) (ports.PolicyRequestRecord, bool, error) {
	if ownerUserID == "" || idempotencyKey == "" {
		return ports.PolicyRequestRecord{}, false, domain.ErrInvalid
	}
	return s.repository.GetPolicyRequest(ctx, ownerUserID, idempotencyKey)
}

// SetPolicyInput is one owner-visible SetAppPolicy command.
type SetPolicyInput struct {
	OwnerUserID            string
	ProjectID              string
	AppInstanceID          string
	Spec                   domain.PolicySpec
	ExpectedPolicyRevision int64
	IdempotencyKey         string
}
