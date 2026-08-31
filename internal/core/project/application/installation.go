package application

import (
	"context"
	"errors"
	"strconv"
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

// TransitionInput is one validated-boundary explicit version transition
// command (ADR-0012).
type TransitionInput struct {
	OwnerUserID      string
	IdempotencyKey   string
	ProjectID        string
	InstallationID   string
	ExpectedRevision int64
	Version          string
}

// RollbackInput is one validated-boundary previous-pinned-version rollback
// command (ADR-0012). The request carries no target: Core derives it from
// the installation's durable history.
type RollbackInput struct {
	OwnerUserID      string
	IdempotencyKey   string
	ProjectID        string
	InstallationID   string
	ExpectedRevision int64
}

// VersionHistoryPage is one page of an installation's version history,
// oldest first, with the next cursor as an opaque decimal sequence string.
type VersionHistoryPage struct {
	Items     []domain.VersionSnapshot
	NextToken string
}

const versionHistoryMaxPageSize = 20

// Transition pins one explicit immutable registry version onto the active
// installation. The adjudication order is fixed: validate, digest the
// canonical client request, replay a consumed key before any resolution,
// read the owner-scoped active installation, resolve the exact target
// version through the neutral catalog port, verify the pinned identity,
// scope, and grant compatibility (the current grant set must remain a
// subset of the target's requested permissions — permissions are never
// expanded), then hand the command to the repository, which re-arbitrates
// everything under the project lock in one transaction.
func (s *InstallationService) Transition(ctx context.Context, input TransitionInput) (ports.InstallationResult, error) {
	if input.OwnerUserID == "" || !domain.ValidInstallationIdempotencyKey(input.IdempotencyKey) ||
		!domain.ValidInstallationUUID(input.ProjectID) || !domain.ValidInstallationUUID(input.InstallationID) ||
		input.ExpectedRevision <= 0 || !domain.ValidInstallationVersion(input.Version) {
		return ports.InstallationResult{}, domain.ErrInvalid
	}
	digest := domain.TransitionRequestDigest(input.ProjectID, input.InstallationID, input.ExpectedRevision, input.Version)
	if result, found, err := s.replayIfConsumed(ctx, input.OwnerUserID, input.IdempotencyKey, digest); found || err != nil {
		return result, err
	}
	installation, err := s.repository.ResolveActiveInstallation(ctx, input.OwnerUserID, input.ProjectID, input.InstallationID)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	target, err := s.resolveCompatibleTarget(ctx, input.OwnerUserID, installation, input.Version)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	return s.repository.Transition(ctx, ports.TransitionCommand{
		OwnerUserID: input.OwnerUserID, IdempotencyKey: input.IdempotencyKey,
		ProjectID: input.ProjectID, InstallationID: input.InstallationID,
		Target: target, Source: domain.VersionSourceTransition,
		ExpectedRevision: input.ExpectedRevision, RequestDigest: digest, Now: s.now(),
	})
}

// Rollback restores the most recent previous pinned snapshot that differs
// from the current (version, digest). The target is derived twice from the
// durable history — once here for registry verification and grant
// compatibility, and once again inside the repository transaction under the
// project lock — so a concurrent version change between the two reads can
// never pin an unverified target: the loser is a stable conflict, never a
// silent mismatch.
func (s *InstallationService) Rollback(ctx context.Context, input RollbackInput) (ports.InstallationResult, error) {
	if input.OwnerUserID == "" || !domain.ValidInstallationIdempotencyKey(input.IdempotencyKey) ||
		!domain.ValidInstallationUUID(input.ProjectID) || !domain.ValidInstallationUUID(input.InstallationID) ||
		input.ExpectedRevision <= 0 {
		return ports.InstallationResult{}, domain.ErrInvalid
	}
	digest := domain.RollbackRequestDigest(input.ProjectID, input.InstallationID, input.ExpectedRevision)
	if result, found, err := s.replayIfConsumed(ctx, input.OwnerUserID, input.IdempotencyKey, digest); found || err != nil {
		return result, err
	}
	installation, err := s.repository.ResolveActiveInstallation(ctx, input.OwnerUserID, input.ProjectID, input.InstallationID)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	history, err := s.repository.ListAllVersions(ctx, input.OwnerUserID, input.InstallationID)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	if err := domain.ValidateVersionHistory(history); err != nil {
		return ports.InstallationResult{}, err
	}
	candidate, err := deriveRollbackTarget(history, installation)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	target, err := s.resolveCompatibleTarget(ctx, input.OwnerUserID, installation, candidate.Version)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	// The registry version must be byte-identical to the durable history
	// snapshot: registry versions are immutable, so any drift is corruption.
	if target.Version != candidate.Version || target.ManifestDigest != candidate.ManifestDigest {
		return ports.InstallationResult{}, errPinnedIdentityDrift
	}
	return s.repository.Transition(ctx, ports.TransitionCommand{
		OwnerUserID: input.OwnerUserID, IdempotencyKey: input.IdempotencyKey,
		ProjectID: input.ProjectID, InstallationID: input.InstallationID,
		Target: target, Source: domain.VersionSourceRollback,
		ExpectedRevision: input.ExpectedRevision, RequestDigest: digest, Now: s.now(),
	})
}

// ListVersionHistory returns one page of the installation's version history,
// oldest first. The read is owner/project/active-installation scoped and the
// stored snapshots are re-validated on every read; corruption is a sanitized
// Internal, never a silently trimmed page.
func (s *InstallationService) ListVersionHistory(ctx context.Context, ownerUserID, projectID, installationID, cursor string, pageSize int) (VersionHistoryPage, error) {
	if ownerUserID == "" || !domain.ValidInstallationUUID(projectID) || !domain.ValidInstallationUUID(installationID) {
		return VersionHistoryPage{}, domain.ErrInvalid
	}
	var after int64
	if cursor != "" {
		value, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || value < 0 {
			return VersionHistoryPage{}, domain.ErrInvalid
		}
		after = value
	}
	switch {
	case pageSize < 0:
		return VersionHistoryPage{}, domain.ErrInvalid
	case pageSize == 0:
		pageSize = installationDefaultPageSize
	case pageSize > versionHistoryMaxPageSize:
		pageSize = versionHistoryMaxPageSize
	}
	// The active-installation resolution carries the owner/project/archive
	// scoping: history of a tombstoned or foreign installation is NotFound,
	// exactly like every other installation read.
	if _, err := s.repository.ResolveActiveInstallation(ctx, ownerUserID, projectID, installationID); err != nil {
		return VersionHistoryPage{}, err
	}
	items, err := s.repository.ListAllVersions(ctx, ownerUserID, installationID)
	if err != nil {
		return VersionHistoryPage{}, err
	}
	if err := domain.ValidateVersionHistory(items); err != nil {
		return VersionHistoryPage{}, err
	}
	filtered := make([]domain.VersionSnapshot, 0, len(items))
	for _, snapshot := range items {
		if snapshot.Sequence > after {
			filtered = append(filtered, snapshot)
		}
	}
	if len(filtered) <= pageSize {
		return VersionHistoryPage{Items: filtered, NextToken: ""}, nil
	}
	page := filtered[:pageSize]
	return VersionHistoryPage{
		Items:     page,
		NextToken: strconv.FormatInt(page[len(page)-1].Sequence, 10),
	}, nil
}

// resolveCompatibleTarget resolves one exact registry version for the
// installation's app and verifies the identity, scope, and grant
// compatibility invariants shared by transition and rollback.
func (s *InstallationService) resolveCompatibleTarget(ctx context.Context, ownerUserID string, installation domain.Installation, version string) (domain.PinnedApp, error) {
	pinned, err := s.catalog.Resolve(ctx, ownerUserID, installation.AppID, version)
	if err != nil {
		return domain.PinnedApp{}, err
	}
	if pinned.AppID != installation.AppID || !domain.ValidInstallationVersion(pinned.Version) ||
		!domain.ValidInstallationManifestDigest(pinned.ManifestDigest) {
		return domain.PinnedApp{}, errCatalogCorrupt
	}
	if !domain.InstallableScope(pinned.Scope) {
		return domain.PinnedApp{}, errAppScopeViolated
	}
	if err := domain.GrantsCompatibleWithTarget(installation.GrantedPermissions, pinned.Permissions); err != nil {
		return domain.PinnedApp{}, err
	}
	return pinned, nil
}

// deriveRollbackTarget selects the most recent history snapshot whose
// (version, digest) differs from the installation's current pinned identity.
func deriveRollbackTarget(history []domain.VersionSnapshot, installation domain.Installation) (domain.VersionSnapshot, error) {
	for index := len(history) - 1; index >= 0; index-- {
		snapshot := history[index]
		if snapshot.Version != installation.Version || snapshot.ManifestDigest != installation.ManifestDigest {
			return snapshot, nil
		}
	}
	return domain.VersionSnapshot{}, domain.ErrNoPreviousVersion
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
