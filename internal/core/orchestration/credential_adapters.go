// Credential Vault composition adapters (ADR-0009). These narrow types wrap
// the Credential application service for the ports other Core modules
// declare. A nil vault (no master key / admin socket configured) yields
// fail-closed adapters: providers that require a credential are never
// selectable, no snapshot resolves, and snapshot verification fails.
package orchestration

import (
	"context"
	"errors"

	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	credentialapp "github.com/yangtao121/workos/internal/core/credential/application"
	credentialdomain "github.com/yangtao121/workos/internal/core/credential/domain"
	catalogports "github.com/yangtao121/workos/internal/core/harnesscatalog/ports"
)

// vaultAvailability adapts the vault to the catalog's owner-aware overlay.
type vaultAvailability struct{ service *credentialapp.Service }

// NewCredentialAvailability returns the catalog overlay port. A nil vault
// (vault not configured) yields an adapter that reports every consumer
// unavailable — the fail-closed direction.
func NewCredentialAvailability(service *credentialapp.Service) catalogports.CredentialAvailability {
	return vaultAvailability{service: service}
}

func (a vaultAvailability) Available(ctx context.Context, ownerUserID, consumerID string) (bool, error) {
	if a.service == nil {
		return false, nil
	}
	_, err := a.service.ActiveCredential(ctx, ownerUserID, consumerID, credentialdomain.PurposeProviderAPIKeyV1)
	if errors.Is(err, credentialdomain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// vaultSnapshots adapts the vault to the task router's snapshot resolver.
type vaultSnapshots struct{ service *credentialapp.Service }

// NewCredentialSnapshots returns the router's resolver. A nil vault yields a
// resolver that always answers ErrNotFound — credential-bearing providers
// are never admitted.
func NewCredentialSnapshots(service *credentialapp.Service) agentports.CredentialSnapshots {
	return vaultSnapshots{service: service}
}

func (s vaultSnapshots) ActiveSnapshot(ctx context.Context, ownerUserID, consumerID string) (agentports.CredentialSnapshotRef, error) {
	if s.service == nil {
		return agentports.CredentialSnapshotRef{}, agentdomain.ErrNotFound
	}
	credential, err := s.service.ActiveCredential(ctx, ownerUserID, consumerID, credentialdomain.PurposeProviderAPIKeyV1)
	if errors.Is(err, credentialdomain.ErrNotFound) {
		return agentports.CredentialSnapshotRef{}, agentdomain.ErrNotFound
	}
	if err != nil {
		return agentports.CredentialSnapshotRef{}, err
	}
	return agentports.CredentialSnapshotRef{CredentialID: credential.ID, Revision: credential.Revision}, nil
}

// vaultVerifier adapts the vault to approval-time snapshot re-verification.
type vaultVerifier struct{ service *credentialapp.Service }

// NewCredentialSnapshotVerifier returns the approval verifier. A nil vault
// yields a verifier that always fails — approvals for credential-bearing
// tasks stay pending instead of silently running.
func NewCredentialSnapshotVerifier(service *credentialapp.Service) agentports.CredentialSnapshotVerifier {
	return vaultVerifier{service: service}
}

func (v vaultVerifier) VerifySnapshot(ctx context.Context, ownerUserID, consumerID, credentialID string, revision int64) error {
	if v.service == nil {
		return agentdomain.ErrLeaseLost
	}
	if err := v.service.AsSnapshotVerifier().VerifySnapshot(ctx, ownerUserID, consumerID, credentialID, revision); err != nil {
		if errors.Is(err, credentialdomain.ErrLeaseLost) {
			return agentdomain.ErrLeaseLost
		}
		return err
	}
	return nil
}
