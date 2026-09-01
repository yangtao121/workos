package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
)

const (
	testOwner    = "01999999-9999-7999-8999-999999999991"
	testProject  = "01999999-9999-7999-8999-999999999992"
	testInstall  = "01999999-9999-7999-8999-999999999993"
	otherProject = "01999999-9999-7999-8999-999999999994"
)

var installationTestTime = time.Date(2026, 8, 31, 12, 0, 0, 123000000, time.UTC)

type fakeRepository struct {
	requests   map[string]ports.StoredInstallationRequest
	byID       map[string]domain.Installation
	installLog []ports.InstallCommand
	listed     []domain.Installation
	listErr    error
	resolveErr error
	// setLog records every SetAppGrants command that reached the repository.
	setLog []ports.SetAppGrantsCommand
	// transitionLog records every version command; versions backs
	// ListAllVersions.
	transitionLog []ports.TransitionCommand
	versions      []domain.VersionSnapshot
	// setFn, when set, replaces the simulated transaction so failure paths
	// can be injected.
	setFn func(command ports.SetAppGrantsCommand) (ports.InstallationResult, error)
	// projectRevision models the Project aggregate revision the transaction
	// domain would move; events/outbox mirror the same-commit facts.
	projectRevision int64
	events          []string
	outbox          []string
}

func (f *fakeRepository) LookupInstallationRequest(_ context.Context, _, key string) (ports.StoredInstallationRequest, bool, error) {
	stored, ok := f.requests[key]
	return stored, ok, nil
}

func (f *fakeRepository) ResolveActiveInstallation(_ context.Context, ownerUserID, projectID, installationID string) (domain.Installation, error) {
	if f.resolveErr != nil {
		return domain.Installation{}, f.resolveErr
	}
	installation, ok := f.byID[installationID]
	if !ok || installation.OwnerUserID != ownerUserID || installation.ProjectID != projectID || installation.UninstalledAt != nil {
		return domain.Installation{}, domain.ErrNotFound
	}
	return installation, nil
}

func (f *fakeRepository) GetInstallation(_ context.Context, _, installationID string) (domain.Installation, error) {
	installation, ok := f.byID[installationID]
	if !ok {
		return domain.Installation{}, domain.ErrNotFound
	}
	return installation, nil
}

func (f *fakeRepository) Install(_ context.Context, command ports.InstallCommand) (ports.InstallationResult, error) {
	f.installLog = append(f.installLog, command)
	return ports.InstallationResult{Installation: domain.Installation{
		ID: command.NewInstallationID, OwnerUserID: command.OwnerUserID, ProjectID: command.ProjectID,
		AppID: command.AppID, Version: command.Pinned.Version, ManifestDigest: command.Pinned.ManifestDigest,
		GrantRevision: 1,
		InstalledAt:   command.Now,
	}, ProjectRevision: command.ExpectedRevision + 1}, nil
}

func (f *fakeRepository) Uninstall(_ context.Context, command ports.UninstallCommand) (ports.InstallationResult, error) {
	installation, ok := f.byID[command.InstallationID]
	if !ok {
		return ports.InstallationResult{}, domain.ErrNotFound
	}
	tombstone := command.Now
	installation.UninstalledAt = &tombstone
	return ports.InstallationResult{Installation: installation, ProjectRevision: command.ExpectedRevision + 1}, nil
}

// SetAppGrants simulates the repository transaction contract: deterministic
// no-op (key consumed, nothing moves) or the atomic real change (grant
// revision +1, Project revision +1, exactly one grants-updated event and
// outbox row, key consumed with the new snapshot).
func (f *fakeRepository) SetAppGrants(_ context.Context, command ports.SetAppGrantsCommand) (ports.InstallationResult, error) {
	f.setLog = append(f.setLog, command)
	if f.setFn != nil {
		return f.setFn(command)
	}
	installation, ok := f.byID[command.InstallationID]
	if !ok || installation.OwnerUserID != command.OwnerUserID || installation.ProjectID != command.ProjectID || installation.UninstalledAt != nil {
		return ports.InstallationResult{}, domain.ErrNotFound
	}
	if f.projectRevision != 0 && f.projectRevision != command.ExpectedRevision {
		return ports.InstallationResult{}, domain.ErrConflict
	}
	if equalCanonicalGrants(installation.GrantedPermissions, command.GrantedPermissions) {
		f.requests[command.IdempotencyKey] = ports.StoredInstallationRequest{
			Command: "set-grants", RequestDigest: command.RequestDigest, InstallationID: installation.ID,
			ProjectRevision:          command.ExpectedRevision,
			ResultGrantedPermissions: installation.GrantedPermissions,
			ResultGrantRevision:      installation.GrantRevision,
			ResultVersion:            installation.Version,
			ResultManifestDigest:     installation.ManifestDigest,
		}
		return ports.InstallationResult{Installation: installation, ProjectRevision: command.ExpectedRevision}, nil
	}
	installation.GrantedPermissions = command.GrantedPermissions
	installation.GrantRevision++
	f.byID[installation.ID] = installation
	newRevision := command.ExpectedRevision + 1
	f.projectRevision = newRevision
	f.events = append(f.events, "project.app.grants.updated.v1")
	f.outbox = append(f.outbox, "project.app.grants.updated.v1")
	f.requests[command.IdempotencyKey] = ports.StoredInstallationRequest{
		Command: "set-grants", RequestDigest: command.RequestDigest, InstallationID: installation.ID,
		ProjectRevision:          newRevision,
		ResultGrantedPermissions: installation.GrantedPermissions,
		ResultGrantRevision:      installation.GrantRevision,
		ResultVersion:            installation.Version,
		ResultManifestDigest:     installation.ManifestDigest,
	}
	return ports.InstallationResult{Installation: installation, ProjectRevision: newRevision}, nil
}

// equalCanonicalGrants compares two already-canonical grant sets in the shape
// the repository contract guarantees.
func equalCanonicalGrants(stored, target []string) bool {
	if len(stored) != len(target) {
		return false
	}
	for index := range stored {
		if stored[index] != target[index] {
			return false
		}
	}
	return true
}

func (f *fakeRepository) Transition(_ context.Context, command ports.TransitionCommand) (ports.InstallationResult, error) {
	f.transitionLog = append(f.transitionLog, command)
	installation, ok := f.byID[command.InstallationID]
	if !ok {
		return ports.InstallationResult{}, domain.ErrNotFound
	}
	updated := installation
	updated.Version = command.Target.Version
	updated.ManifestDigest = command.Target.ManifestDigest
	f.byID[command.InstallationID] = updated
	return ports.InstallationResult{Installation: updated, ProjectRevision: command.ExpectedRevision + 1}, nil
}

func (f *fakeRepository) ListAllVersions(_ context.Context, _, _ string) ([]domain.VersionSnapshot, error) {
	return f.versions, nil
}

func (f *fakeRepository) ListActive(_ context.Context, _, _, _ string, limit int) ([]domain.Installation, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.listed) > limit {
		return f.listed[:limit], nil
	}
	return f.listed, nil
}

type fakeCatalog struct {
	resolved map[string]domain.PinnedApp
	err      error
	calls    int
}

func (f *fakeCatalog) Resolve(_ context.Context, _, appID, version string) (domain.PinnedApp, error) {
	f.calls++
	if f.err != nil {
		return domain.PinnedApp{}, f.err
	}
	return f.resolved[appID+"@"+version], nil
}

type staticIDs struct{ next int }

func (s *staticIDs) New() string {
	s.next++
	return fmt.Sprintf("01999999-9999-7999-8999-%012d", s.next)
}

func newInstallationService(t *testing.T, repo ports.InstallationRepository, catalog AppCatalog) *InstallationService {
	t.Helper()
	service, err := NewInstallationService(repo, catalog, &staticIDs{})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func pinned(version, digest, scope string) domain.PinnedApp {
	return domain.PinnedApp{AppID: "board-app", Version: version, ManifestDigest: digest, Scope: scope}
}

func digestOf(char byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = char
	}
	return "sha256:" + string(value)
}

func installInput(key string) InstallInput {
	return InstallInput{OwnerUserID: testOwner, IdempotencyKey: key, ProjectID: testProject, AppID: "board-app", ExpectedRevision: 4}
}

func TestInstallRejectsMalformedRequests(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{requests: map[string]ports.StoredInstallationRequest{}, byID: map[string]domain.Installation{}}
	catalog := &fakeCatalog{resolved: map[string]domain.PinnedApp{"board-app@": pinned("1.0.0", digestOf('a'), "user")}}
	service := newInstallationService(t, repo, catalog)
	cases := []InstallInput{
		{OwnerUserID: "", IdempotencyKey: "k", ProjectID: testProject, AppID: "board-app", ExpectedRevision: 1},
		{OwnerUserID: testOwner, IdempotencyKey: "", ProjectID: testProject, AppID: "board-app", ExpectedRevision: 1},
		{OwnerUserID: testOwner, IdempotencyKey: "k\n", ProjectID: testProject, AppID: "board-app", ExpectedRevision: 1},
		{OwnerUserID: testOwner, IdempotencyKey: "k", ProjectID: "not-a-uuid", AppID: "board-app", ExpectedRevision: 1},
		{OwnerUserID: testOwner, IdempotencyKey: "k", ProjectID: testProject, AppID: "Bad_ID", ExpectedRevision: 1},
		{OwnerUserID: testOwner, IdempotencyKey: "k", ProjectID: testProject, AppID: "board-app", Version: "01.2.3", ExpectedRevision: 1},
		{OwnerUserID: testOwner, IdempotencyKey: "k", ProjectID: testProject, AppID: "board-app", ExpectedRevision: 0},
	}
	for index, input := range cases {
		if _, err := service.Install(context.Background(), input); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("case %d must be InvalidArgument, got %v", index, err)
		}
	}
	if len(repo.installLog) != 0 || catalog.calls != 0 {
		t.Fatal("invalid requests must not reach the catalog or repository")
	}
}

func TestInstallResolvesCurrentAndPinsResolvedVersion(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{requests: map[string]ports.StoredInstallationRequest{}, byID: map[string]domain.Installation{}}
	catalog := &fakeCatalog{resolved: map[string]domain.PinnedApp{"board-app@": pinned("2.0.0", digestOf('b'), "project")}}
	service := newInstallationService(t, repo, catalog)
	result, err := service.Install(context.Background(), installInput("install-once"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Installation.Version != "2.0.0" || result.Installation.ManifestDigest != digestOf('b') {
		t.Fatalf("installation must pin the resolved version: %#v", result.Installation)
	}
	if result.ProjectRevision != 5 {
		t.Fatalf("revision must advance by one, got %d", result.ProjectRevision)
	}
	command := repo.installLog[0]
	if command.RequestDigest != domain.InstallationRequestDigest("install", testProject, "board-app", "", "", 4) {
		t.Fatal("repository must receive the canonical client digest")
	}
}

func TestInstallExplicitVersionPassesExactVersionToCatalog(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{requests: map[string]ports.StoredInstallationRequest{}, byID: map[string]domain.Installation{}}
	catalog := &fakeCatalog{resolved: map[string]domain.PinnedApp{"board-app@1.9.0": pinned("1.9.0", digestOf('c'), "user")}}
	service := newInstallationService(t, repo, catalog)
	input := installInput("install-explicit")
	input.Version = "1.9.0"
	result, err := service.Install(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Installation.Version != "1.9.0" {
		t.Fatalf("explicit version must pin exactly, got %q", result.Installation.Version)
	}
}

func TestInstallExplicitVersionRejectsCatalogIdentityDrift(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{requests: map[string]ports.StoredInstallationRequest{}, byID: map[string]domain.Installation{}}
	catalog := &fakeCatalog{resolved: map[string]domain.PinnedApp{"board-app@1.9.0": pinned("2.0.0", digestOf('c'), "user")}}
	service := newInstallationService(t, repo, catalog)
	input := installInput("install-drifted")
	input.Version = "1.9.0"
	if _, err := service.Install(context.Background(), input); !errors.Is(err, errCatalogCorrupt) {
		t.Fatalf("catalog must return the exact requested version, got %v", err)
	}
	if len(repo.installLog) != 0 {
		t.Fatal("a drifted catalog identity must not reach the repository")
	}
}

func TestInstallCatalogDenialIsNotFound(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{requests: map[string]ports.StoredInstallationRequest{}, byID: map[string]domain.Installation{}}
	service := newInstallationService(t, repo, &fakeCatalog{err: ErrAppNotInstallable})
	if _, err := service.Install(context.Background(), installInput("install-missing")); !errors.Is(err, ErrAppNotInstallable) {
		t.Fatalf("catalog denial must surface, got %v", err)
	}
	if len(repo.installLog) != 0 {
		t.Fatal("denied install must not reach the repository")
	}
}

func TestInstallFailsClosedOnNonInstallableScope(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{requests: map[string]ports.StoredInstallationRequest{}, byID: map[string]domain.Installation{}}
	catalog := &fakeCatalog{resolved: map[string]domain.PinnedApp{"board-app@": pinned("1.0.0", digestOf('d'), "system")}}
	service := newInstallationService(t, repo, catalog)
	result, err := service.Install(context.Background(), installInput("install-system"))
	if err == nil || result.Installation.ID != "" {
		t.Fatalf("system scope must fail closed: %#v err=%v", result, err)
	}
	if len(repo.installLog) != 0 {
		t.Fatal("fail-closed install must not reach the repository")
	}
}

func TestInstallReplaysConsumedKeyBeforeResolvingCurrent(t *testing.T) {
	t.Parallel()
	stored := ports.StoredInstallationRequest{
		Command: "install", RequestDigest: domain.InstallationRequestDigest("install", testProject, "board-app", "", "", 4),
		InstallationID: testInstall, ProjectRevision: 5,
		// Migration 025 backfilled every mapping with the pinned identity its
		// first response carried; the replay projection includes it.
		ResultGrantRevision: 1, ResultVersion: "1.0.0", ResultManifestDigest: digestOf('e'),
	}
	repo := &fakeRepository{
		requests: map[string]ports.StoredInstallationRequest{"install-once": stored},
		byID: map[string]domain.Installation{testInstall: {
			ID: testInstall, OwnerUserID: testOwner, ProjectID: testProject, AppID: "board-app",
			Version: "1.0.0", ManifestDigest: digestOf('e'),
			// A later uninstall tombstoned the row; the replay must still
			// present the first response's active projection.
			InstalledAt:   installationTestTime,
			UninstalledAt: ptr(installationTestTime.Add(time.Minute)),
		}},
	}
	catalog := &fakeCatalog{resolved: map[string]domain.PinnedApp{"board-app@": pinned("9.9.9", digestOf('f'), "user")}}
	service := newInstallationService(t, repo, catalog)
	result, err := service.Install(context.Background(), installInput("install-once"))
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectRevision != 5 || result.Installation.Version != "1.0.0" || result.Installation.UninstalledAt != nil {
		t.Fatalf("replay must return the first result verbatim: %#v", result)
	}
	if catalog.calls != 0 {
		t.Fatal("replay must not re-resolve the registry current")
	}
}

func TestInstallConsumedKeyDifferentRequestConflicts(t *testing.T) {
	t.Parallel()
	stored := ports.StoredInstallationRequest{
		Command: "install", RequestDigest: domain.InstallationRequestDigest("install", testProject, "board-app", "", "", 4),
		InstallationID: testInstall, ProjectRevision: 5,
		// Migration 025 backfilled every mapping with the pinned identity its
		// first response carried; the replay projection includes it.
		ResultVersion: "1.0.0", ResultManifestDigest: digestOf('e'),
	}
	repo := &fakeRepository{requests: map[string]ports.StoredInstallationRequest{"install-once": stored}, byID: map[string]domain.Installation{testInstall: {ID: testInstall}}}
	service := newInstallationService(t, repo, &fakeCatalog{})
	different := installInput("install-once")
	different.ExpectedRevision = 5
	if _, err := service.Install(context.Background(), different); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same key different request must be Aborted, got %v", err)
	}
}

func TestUninstallReplayAndValidation(t *testing.T) {
	t.Parallel()
	tombstone := installationTestTime.Add(time.Minute)
	stored := ports.StoredInstallationRequest{
		Command:        "uninstall",
		RequestDigest:  domain.InstallationRequestDigest("uninstall", testProject, "", "", testInstall, 4),
		InstallationID: testInstall, ProjectRevision: 5, ResultUninstalledAt: &tombstone,
		ResultGrantRevision: 1, ResultVersion: "1.0.0", ResultManifestDigest: digestOf('f'),
	}
	repo := &fakeRepository{
		requests: map[string]ports.StoredInstallationRequest{"uninstall-once": stored},
		byID: map[string]domain.Installation{testInstall: {
			ID: testInstall, OwnerUserID: testOwner, ProjectID: testProject, AppID: "board-app",
			Version: "1.0.0", ManifestDigest: digestOf('f'), InstalledAt: tombstone.Add(-time.Hour),
			UninstalledAt: &tombstone,
		}},
	}
	service := newInstallationService(t, repo, &fakeCatalog{})
	result, err := service.Uninstall(context.Background(), UninstallInput{
		OwnerUserID: testOwner, IdempotencyKey: "uninstall-once", ProjectID: testProject,
		InstallationID: testInstall, ExpectedRevision: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectRevision != 5 || result.Installation.UninstalledAt == nil || !result.Installation.UninstalledAt.Equal(tombstone) {
		t.Fatalf("uninstall replay must return the tombstoned first result: %#v", result)
	}
	// Malformed uninstall inputs are InvalidArgument.
	for _, input := range []UninstallInput{
		{OwnerUserID: "", IdempotencyKey: "k", ProjectID: testProject, InstallationID: testInstall, ExpectedRevision: 1},
		{OwnerUserID: testOwner, IdempotencyKey: "k", ProjectID: "bad", InstallationID: testInstall, ExpectedRevision: 1},
		{OwnerUserID: testOwner, IdempotencyKey: "k", ProjectID: testProject, InstallationID: "bad", ExpectedRevision: 1},
		{OwnerUserID: testOwner, IdempotencyKey: "k", ProjectID: testProject, InstallationID: testInstall, ExpectedRevision: -1},
	} {
		if _, err := service.Uninstall(context.Background(), input); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("malformed uninstall must be invalid, got %v", err)
		}
	}
}

func TestReplayFailsClosedWhenSnapshotCorruptsInstallation(t *testing.T) {
	t.Parallel()
	stored := ports.StoredInstallationRequest{
		Command: "uninstall", RequestDigest: domain.InstallationRequestDigest("uninstall", testProject, "", "", testInstall, 4),
		InstallationID: testInstall, ProjectRevision: 5,
		ResultUninstalledAt: ptr(installationTestTime.Add(-time.Minute)),
		ResultGrantRevision: 1, ResultVersion: "1.0.0", ResultManifestDigest: digestOf('a'),
	}
	repo := &fakeRepository{
		requests: map[string]ports.StoredInstallationRequest{"corrupt-replay": stored},
		byID: map[string]domain.Installation{testInstall: {
			ID: testInstall, OwnerUserID: testOwner, ProjectID: testProject, AppID: "board-app",
			Version: "1.0.0", ManifestDigest: digestOf('a'), GrantRevision: 1,
			InstalledAt: installationTestTime,
		}},
	}
	service := newInstallationService(t, repo, &fakeCatalog{})
	if _, err := service.Uninstall(context.Background(), UninstallInput{
		OwnerUserID: testOwner, IdempotencyKey: "corrupt-replay", ProjectID: testProject,
		InstallationID: testInstall, ExpectedRevision: 4,
	}); !errors.Is(err, domain.ErrInstallationCorrupt) {
		t.Fatalf("a corrupt first-response overlay must fail closed, got %v", err)
	}
}

func TestListInstalledNormalizesPaging(t *testing.T) {
	t.Parallel()
	var (
		listed []domain.Installation
		value  string
	)
	for index := 0; index < 120; index++ {
		value = fmt.Sprintf("app-%03d", index)
		listed = append(listed, domain.Installation{AppID: value})
	}
	repo := &fakeRepository{requests: map[string]ports.StoredInstallationRequest{}, byID: map[string]domain.Installation{}, listed: listed}
	service := newInstallationService(t, repo, &fakeCatalog{})

	defaultPage, err := service.ListInstalled(context.Background(), testOwner, testProject, "", 0)
	if err != nil || len(defaultPage.Items) != 50 || defaultPage.NextToken != "app-049" {
		t.Fatalf("default page must be 50 items with a token: len=%d token=%q err=%v", len(defaultPage.Items), defaultPage.NextToken, err)
	}
	clamped, err := service.ListInstalled(context.Background(), testOwner, testProject, "", 101)
	if err != nil || len(clamped.Items) != 100 || clamped.NextToken != "app-099" {
		t.Fatalf("page size must clamp to 100: len=%d err=%v", len(clamped.Items), err)
	}
	// Exactly-full final page: the repository returns exactly the limit with
	// no extra row, so no token may be fabricated.
	exact := &fakeRepository{requests: map[string]ports.StoredInstallationRequest{}, byID: map[string]domain.Installation{}, listed: listed[:50]}
	service = newInstallationService(t, exact, &fakeCatalog{})
	finalPage, err := service.ListInstalled(context.Background(), testOwner, testProject, "", 50)
	if err != nil || len(finalPage.Items) != 50 || finalPage.NextToken != "" {
		t.Fatalf("exactly-full page must not fabricate a token: len=%d token=%q err=%v", len(finalPage.Items), finalPage.NextToken, err)
	}
	if _, err := service.ListInstalled(context.Background(), testOwner, testProject, "", -1); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("negative page size must be InvalidArgument, got %v", err)
	}
	if _, err := service.ListInstalled(context.Background(), testOwner, testProject, "not a cursor", 10); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("malformed cursor must be InvalidArgument, got %v", err)
	}
	if _, err := service.ListInstalled(context.Background(), testOwner, "not-a-uuid", "", 10); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("malformed project must be InvalidArgument, got %v", err)
	}
	if _, err := service.ListInstalled(context.Background(), "", testProject, "", 10); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing owner must be InvalidArgument, got %v", err)
	}
}

func ptr(value time.Time) *time.Time { return &value }
