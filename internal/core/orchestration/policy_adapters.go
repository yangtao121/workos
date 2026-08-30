package orchestration

import (
	"context"
	"errors"

	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	catalogapp "github.com/yangtao121/workos/internal/core/harnesscatalog/application"
	catalogdomain "github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

// installationFacts adapts the Project installation authority to the Agent
// module's neutral InstallationSource port. It reuses the same
// ResolveActiveInstallation fact the App Agent authorization uses; no Agent
// code ever touches Project tables or adapters.
type installationFacts struct {
	installations installationSource
}

func NewInstallationFacts(installations installationSource) (*installationFacts, error) {
	if installations == nil {
		return nil, errors.New("installation facts require an installation source")
	}
	return &installationFacts{installations: installations}, nil
}

func (a *installationFacts) ResolveActiveInstallation(ctx context.Context, ownerUserID, projectID, installationID string) (agentports.InstallationFacts, error) {
	installation, err := a.installations.ResolveActiveInstallation(ctx, ownerUserID, projectID, installationID)
	if err != nil {
		// The Project module's not-found must surface as the Agent domain's
		// sentinel so transports answer sanitized NotFound, never Internal.
		if errors.Is(err, projectdomain.ErrNotFound) || errors.Is(err, projectdomain.ErrInvalid) {
			return agentports.InstallationFacts{}, agentdomain.ErrNotFound
		}
		return agentports.InstallationFacts{}, err
	}
	if err := validateStoredGrant(installation.GrantedPermissions); err != nil {
		return agentports.InstallationFacts{}, err
	}
	return agentports.InstallationFacts{
		AppID:              installation.AppID,
		GrantedPermissions: installation.GrantedPermissions,
		GrantRevision:      installation.GrantRevision,
	}, nil
}

// providerCapabilities adapts the harness catalog application service to the
// Agent module's neutral ProviderCatalog port. Unknown providers are
// sanitized NotFound; an unreachable catalog is retryable Unavailable
// semantics via the Agent store sentinel.
type providerCapabilities struct {
	catalog *catalogapp.Service
}

func NewProviderCapabilities(catalog *catalogapp.Service) (*providerCapabilities, error) {
	if catalog == nil {
		return nil, errors.New("provider capabilities require a harness catalog")
	}
	return &providerCapabilities{catalog: catalog}, nil
}

func (a *providerCapabilities) Capabilities(ctx context.Context, providerID string) (agentports.ProviderCapabilities, error) {
	catalog, err := a.catalog.Get(ctx)
	if err != nil {
		if errors.Is(err, catalogdomain.ErrUnavailable) {
			return agentports.ProviderCapabilities{}, agentports.ErrStoreUnavailable
		}
		return agentports.ProviderCapabilities{}, err
	}
	for _, provider := range catalog.Providers {
		if provider.ID != providerID {
			continue
		}
		return agentports.ProviderCapabilities{
			HardTokenBudget:             provider.Capabilities.HardTokenBudget,
			HardRuntimeDeadline:         provider.Capabilities.HardRuntimeDeadline,
			UsageReporting:              provider.Capabilities.UsageReporting,
			MaxOutputTokens:             provider.Capabilities.MaxOutputTokens,
			MaxRuntimeSeconds:           provider.Capabilities.MaxRuntimeSeconds,
			StructuredArtifacts:         provider.Capabilities.StructuredArtifacts,
			SupportedArtifactTypes:      provider.Capabilities.SupportedArtifactTypes,
			SupportedContextRefTypes:    provider.Capabilities.SupportedContextRefTypes,
			RequiresTaskCredentialLease: provider.Capabilities.RequiresTaskCredentialLease,
		}, nil
	}
	return agentports.ProviderCapabilities{}, agentdomain.ErrNotFound
}

var (
	_ agentports.InstallationSource = (*installationFacts)(nil)
	_ agentports.ProviderCatalog    = (*providerCapabilities)(nil)
)
