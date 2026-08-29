package application

import (
	"context"
	"errors"
	"fmt"
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
// The stored project binding is verified against the active installation's
// project — a row bound to a different project is corruption and fails
// closed, never a silently re-bound policy.
func (s *PolicyService) EffectivePolicy(ctx context.Context, ownerUserID, projectID, appInstanceID string) (domain.Policy, error) {
	policy, facts, err := effectivePolicy(ctx, s.repository, s.installations, ownerUserID, projectID, appInstanceID)
	if err != nil {
		return domain.Policy{}, err
	}
	policy.AppID = facts.AppID
	return policy, nil
}

// effectivePolicy is the shared read path for every policy consumer:
// installation liveness first, then the explicit row — whose project binding
// must equal the installation's project — or the versioned system default.
func effectivePolicy(ctx context.Context, repository ports.Repository, installations ports.InstallationSource, ownerUserID, projectID, appInstanceID string) (domain.Policy, ports.InstallationFacts, error) {
	if ownerUserID == "" || !domain.ValidAppTaskUUID(projectID) || !domain.ValidAppTaskUUID(appInstanceID) {
		return domain.Policy{}, ports.InstallationFacts{}, domain.ErrInvalid
	}
	facts, err := installations.ResolveActiveInstallation(ctx, ownerUserID, projectID, appInstanceID)
	if err != nil {
		return domain.Policy{}, ports.InstallationFacts{}, err
	}
	policy, found, err := repository.GetPolicy(ctx, ownerUserID, appInstanceID)
	if err != nil {
		return domain.Policy{}, ports.InstallationFacts{}, err
	}
	if found {
		// Format-valid project values are not enough: a misbound or drifted
		// row must never serve this installation under a silently rewritten
		// binding.
		if policy.ProjectID != projectID {
			return domain.Policy{}, ports.InstallationFacts{}, fmt.Errorf("policy project binding: %w", domain.ErrPolicyCorrupt)
		}
	} else {
		policy = domain.SystemDefaultPolicy()
	}
	policy.OwnerUserID = ownerUserID
	policy.AppInstanceID = appInstanceID
	policy.ProjectID = projectID
	return policy, facts, nil
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
