package application

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// ErrAppNotInstallable marks a catalog denial — unknown app, unknown
// version, or a foreign owner's app. Transport maps it to a sanitized
// NotFound so the install path is not an existence oracle.
var ErrAppNotInstallable = errors.New("app is not available for installation")

// errAppScopeViolated is the fail-closed verdict for a catalog result that
// claims a non-installable scope (system/trusted). The public registry never
// accepts those, so reaching this error means an invariant is broken; the
// sanitized internal mapping keeps the install path from becoming a bypass.
var errAppScopeViolated = errors.New("resolved app scope is not installable")

// errCatalogCorrupt marks a catalog result whose pinned identity fields do
// not satisfy the shared grammar; like a scope violation it can only mean a
// broken invariant upstream.
var errCatalogCorrupt = errors.New("resolved app reference is malformed")

// errPinnedIdentityDrift marks a catalog result that no longer matches the
// installation's pinned identity facts. Registry versions are immutable and
// installations are immutable in identity, so drift can only be corruption;
// the verdict stays a sanitized Internal and never authorizes a grant change
// against different facts.
var errPinnedIdentityDrift = errors.New("installation pinned identity drifted")

// AppCatalog resolves one installable registry version for the owner. It is
// the neutral application port: the orchestration layer adapts the App
// Registry application service, and the Project module never touches
// registry adapters, SQL, or the canonical manifest.
type AppCatalog interface {
	Resolve(ctx context.Context, ownerUserID, appID, version string) (domain.PinnedApp, error)
}

// InstallationPage is the explicit paging contract mirroring the registry:
// items plus the next cursor as decided by the repository's limit+1 probe.
type InstallationPage struct {
	Items     []domain.Installation
	NextToken string
}

const (
	installationDefaultPageSize = 50
	installationMaxPageSize     = 100
)

// InstallationService executes install/uninstall commands and bounded reads
// against Project-owned installation facts.
type InstallationService struct {
	repository ports.InstallationRepository
	catalog    AppCatalog
	ids        ids.Generator
	now        func() time.Time
}

func NewInstallationService(repository ports.InstallationRepository, catalog AppCatalog, generator ids.Generator) (*InstallationService, error) {
	if repository == nil || catalog == nil || generator == nil {
		return nil, errors.New("installation service requires repository, catalog, and id generator")
	}
	return &InstallationService{repository: repository, catalog: catalog, ids: generator, now: func() time.Time { return time.Now().UTC() }}, nil
}

// InstallInput is one validated-boundary install command request.
type InstallInput struct {
	OwnerUserID      string
	IdempotencyKey   string
	ProjectID        string
	AppID            string
	Version          string
	ExpectedRevision int64
	// GrantedPermissions is the client-submitted grant snapshot in client
	// order; the install path canonicalizes it (sort/dedupe/grammar) before
	// it can become a durable fact and checks it against the pinned
	// version's requested permissions.
	GrantedPermissions []string
}

// UninstallInput is one validated-boundary uninstall command request.
type UninstallInput struct {
	OwnerUserID      string
	IdempotencyKey   string
	ProjectID        string
	InstallationID   string
	ExpectedRevision int64
}

// SetAppGrantsInput is one validated-boundary full-replacement grant command
// request. GrantedPermissions is the complete target set in client order;
// empty means revoke all and never falls back to requested permissions.
type SetAppGrantsInput struct {
	OwnerUserID        string
	IdempotencyKey     string
	ProjectID          string
	InstallationID     string
	ExpectedRevision   int64
	GrantedPermissions []string
}

// Install pins one registry version into the project. The idempotency
// ruling happens before any catalog resolution so an empty requested version
// can never drift between the first attempt and its replay, and the
// repository re-arbitrates the key inside the transaction for concurrent
// same-key commands. The grant snapshot is canonicalized up front (its
// sorted form is part of the request digest) and checked against the pinned
// version's requested permissions after catalog resolution, so a grant can
// never exceed what the exact pinned manifest version asked for.
func (s *InstallationService) Install(ctx context.Context, input InstallInput) (ports.InstallationResult, error) {
	if input.OwnerUserID == "" || !domain.ValidInstallationIdempotencyKey(input.IdempotencyKey) ||
		!domain.ValidInstallationUUID(input.ProjectID) || !domain.ValidInstallationAppID(input.AppID) ||
		(input.Version != "" && !domain.ValidInstallationVersion(input.Version)) || input.ExpectedRevision <= 0 {
		return ports.InstallationResult{}, domain.ErrInvalid
	}
	grant, err := domain.CanonicalGrantShape(input.GrantedPermissions)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	digest := domain.InstallationRequestDigestWithGrants("install", input.ProjectID, input.AppID, input.Version, "", input.ExpectedRevision, grant)
	if result, found, err := s.replayIfConsumed(ctx, input.OwnerUserID, input.IdempotencyKey, digest); found || err != nil {
		return result, err
	}
	pinned, err := s.catalog.Resolve(ctx, input.OwnerUserID, input.AppID, input.Version)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	if pinned.AppID != input.AppID || !domain.ValidInstallationVersion(pinned.Version) ||
		!domain.ValidInstallationManifestDigest(pinned.ManifestDigest) {
		return ports.InstallationResult{}, errCatalogCorrupt
	}
	if !domain.InstallableScope(pinned.Scope) {
		return ports.InstallationResult{}, errAppScopeViolated
	}
	grant, err = domain.CanonicalInstallationGrant(grant, pinned.Permissions)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	return s.repository.Install(ctx, ports.InstallCommand{
		OwnerUserID: input.OwnerUserID, IdempotencyKey: input.IdempotencyKey,
		ProjectID: input.ProjectID, AppID: input.AppID, Pinned: pinned,
		GrantedPermissions: grant,
		ExpectedRevision:   input.ExpectedRevision, RequestDigest: digest,
		NewInstallationID: s.ids.New(), Now: s.now(),
	})
}

// Uninstall tombstones one active installation. No catalog resolution is
// involved: the installation's pinned identity is already a durable fact.
func (s *InstallationService) Uninstall(ctx context.Context, input UninstallInput) (ports.InstallationResult, error) {
	if input.OwnerUserID == "" || !domain.ValidInstallationIdempotencyKey(input.IdempotencyKey) ||
		!domain.ValidInstallationUUID(input.ProjectID) || !domain.ValidInstallationUUID(input.InstallationID) ||
		input.ExpectedRevision <= 0 {
		return ports.InstallationResult{}, domain.ErrInvalid
	}
	digest := domain.InstallationRequestDigest("uninstall", input.ProjectID, "", "", input.InstallationID, input.ExpectedRevision)
	if result, found, err := s.replayIfConsumed(ctx, input.OwnerUserID, input.IdempotencyKey, digest); found || err != nil {
		return result, err
	}
	return s.repository.Uninstall(ctx, ports.UninstallCommand{
		OwnerUserID: input.OwnerUserID, IdempotencyKey: input.IdempotencyKey,
		ProjectID: input.ProjectID, InstallationID: input.InstallationID,
		ExpectedRevision: input.ExpectedRevision, RequestDigest: digest, Now: s.now(),
	})
}

// SetAppGrants replaces one active installation's entire grant set
// (ADR-0003). The adjudication order is fixed: validate and canonicalize the
// target grant, digest the canonical client request, replay an already
// consumed key before any catalog resolution, read the owner-scoped active
// installation, resolve the exact pinned version's requested permissions
// through the neutral catalog port, verify the pinned identity and the
// target-subset rule, then hand the command to the repository, which
// re-arbitrates everything under the project row lock in one transaction.
// The client never submits app identity, requested sets, grant revisions, or
// the new Project revision; all of those are re-derived here and under the
// lock.
func (s *InstallationService) SetAppGrants(ctx context.Context, input SetAppGrantsInput) (ports.InstallationResult, error) {
	if input.OwnerUserID == "" || !domain.ValidInstallationIdempotencyKey(input.IdempotencyKey) ||
		!domain.ValidInstallationUUID(input.ProjectID) || !domain.ValidInstallationUUID(input.InstallationID) ||
		input.ExpectedRevision <= 0 {
		return ports.InstallationResult{}, domain.ErrInvalid
	}
	grant, err := domain.CanonicalGrantShape(input.GrantedPermissions)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	digest := domain.SetGrantsRequestDigest(input.ProjectID, input.InstallationID, input.ExpectedRevision, grant)
	if result, found, err := s.replayIfConsumed(ctx, input.OwnerUserID, input.IdempotencyKey, digest); found || err != nil {
		return result, err
	}
	installation, err := s.repository.ResolveActiveInstallation(ctx, input.OwnerUserID, input.ProjectID, input.InstallationID)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	// Resolve the exact pinned version — never the registry current — so the
	// subset ceiling is the immutable manifest the installation actually
	// pinned at command time.
	pinned, err := s.catalog.Resolve(ctx, input.OwnerUserID, installation.AppID, installation.Version)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	if pinned.AppID != installation.AppID || !domain.ValidInstallationVersion(pinned.Version) ||
		!domain.ValidInstallationManifestDigest(pinned.ManifestDigest) {
		return ports.InstallationResult{}, errCatalogCorrupt
	}
	if pinned.Version != installation.Version || pinned.ManifestDigest != installation.ManifestDigest {
		return ports.InstallationResult{}, errPinnedIdentityDrift
	}
	if !domain.InstallableScope(pinned.Scope) {
		return ports.InstallationResult{}, errAppScopeViolated
	}
	grant, err = domain.CanonicalInstallationGrant(grant, pinned.Permissions)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	return s.repository.SetAppGrants(ctx, ports.SetAppGrantsCommand{
		OwnerUserID: input.OwnerUserID, IdempotencyKey: input.IdempotencyKey,
		ProjectID: input.ProjectID, InstallationID: input.InstallationID,
		Pinned: pinned, GrantedPermissions: grant,
		ExpectedRevision: input.ExpectedRevision, RequestDigest: digest, Now: s.now(),
	})
}

// ResolveActiveInstallation is the authority read for installed-instance
// surface resolution: the installation must be active, belong to the owner's
// project, and sit under a non-archived project. Unknown, foreign, archived,
// or tombstoned instances are sanitized NotFound verdicts.
func (s *InstallationService) ResolveActiveInstallation(ctx context.Context, ownerUserID, projectID, installationID string) (domain.Installation, error) {
	if ownerUserID == "" || !domain.ValidInstallationUUID(projectID) || !domain.ValidInstallationUUID(installationID) {
		return domain.Installation{}, domain.ErrInvalid
	}
	return s.repository.ResolveActiveInstallation(ctx, ownerUserID, projectID, installationID)
}

// ListInstalled returns one page of active installations ordered by app ID.
// The page size is normalized exactly once here: zero means the default,
// values above the maximum clamp to it, negative values are rejected.
func (s *InstallationService) ListInstalled(ctx context.Context, ownerUserID, projectID, cursor string, pageSize int) (InstallationPage, error) {
	if ownerUserID == "" || !domain.ValidInstallationUUID(projectID) {
		return InstallationPage{}, domain.ErrInvalid
	}
	switch {
	case pageSize < 0:
		return InstallationPage{}, domain.ErrInvalid
	case pageSize == 0:
		pageSize = installationDefaultPageSize
	case pageSize > installationMaxPageSize:
		pageSize = installationMaxPageSize
	}
	if cursor != "" && !domain.ValidInstallationAppID(cursor) {
		return InstallationPage{}, domain.ErrInvalid
	}
	items, err := s.repository.ListActive(ctx, ownerUserID, projectID, cursor, pageSize+1)
	if err != nil {
		return InstallationPage{}, err
	}
	if len(items) <= pageSize {
		return InstallationPage{Items: items, NextToken: ""}, nil
	}
	page := items[:pageSize]
	return InstallationPage{Items: page, NextToken: page[len(page)-1].AppID}, nil
}

// replayIfConsumed resolves an already-consumed key: the identical canonical
// request replays the stored first result, anything else conflicts. It runs
// before catalog resolution so registry changes cannot alter replays. The
// persisted result snapshot — tombstone, grant set, and grant epoch — is
// authoritative for the replayed projection, so a later SetAppGrants or
// uninstall can never leak a mutated row into the first response's replay.
func (s *InstallationService) replayIfConsumed(ctx context.Context, ownerUserID, idempotencyKey, digest string) (ports.InstallationResult, bool, error) {
	stored, found, err := s.repository.LookupInstallationRequest(ctx, ownerUserID, idempotencyKey)
	if err != nil || !found {
		return ports.InstallationResult{}, found, err
	}
	if stored.RequestDigest != digest {
		return ports.InstallationResult{}, true, domain.ErrIdempotencyConflict
	}
	installation, err := s.repository.GetInstallation(ctx, ownerUserID, stored.InstallationID)
	if err != nil {
		return ports.InstallationResult{}, true, err
	}
	installation.UninstalledAt = stored.ResultUninstalledAt
	installation.GrantedPermissions = stored.ResultGrantedPermissions
	installation.GrantRevision = stored.ResultGrantRevision
	return ports.InstallationResult{Installation: installation, ProjectRevision: stored.ProjectRevision}, true, nil
}
