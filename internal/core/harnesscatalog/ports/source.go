package ports

import (
	"context"

	"github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
)

type Source interface {
	ListProviders(context.Context) ([]domain.Provider, error)
}

// CredentialAvailability re-resolves, per owner, whether the Credential
// Vault holds an active credential for one consumer. Implementations live in
// the composition layer; the catalog module never touches vault storage.
// Unknown credentials answer (false, nil) — the projection never
// distinguishes "no credential" from storage facts.
type CredentialAvailability interface {
	// Available reports whether the owner holds an active credential for the
	// consumer under the exact purpose the provider declares (ADR-0015).
	Available(ctx context.Context, ownerUserID, consumerID, purpose string) (bool, error)
}
