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

type fakeRepository struct {
	requests   map[string]ports.StoredInstallationRequest
	byID       map[string]domain.Installation
	installLog []ports.InstallCommand
	listed     []domain.Installation
	listErr    error
}

func (f *fakeRepository) LookupInstallationRequest(_ context.Context, _, key string) (ports.StoredInstallationRequest, bool, error) {
	stored, ok := f.requests[key]
	return stored, ok, nil
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
		InstalledAt: command.Now,
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
	}
	repo := &fakeRepository{
		requests: map[string]ports.StoredInstallationRequest{"install-once": stored},
		byID: map[string]domain.Installation{testInstall: {
			ID: testInstall, OwnerUserID: testOwner, ProjectID: testProject, AppID: "board-app",
			Version: "1.0.0", ManifestDigest: digestOf('e'),
			// A later uninstall tombstoned the row; the replay must still
			// present the first response's active projection.
			InstalledAt:   time.Now().UTC(),
			UninstalledAt: ptr(time.Now().UTC().Add(time.Minute)),
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
	tombstone := time.Now().UTC()
	stored := ports.StoredInstallationRequest{
		Command:        "uninstall",
		RequestDigest:  domain.InstallationRequestDigest("uninstall", testProject, "", "", testInstall, 4),
		InstallationID: testInstall, ProjectRevision: 5, ResultUninstalledAt: &tombstone,
	}
	repo := &fakeRepository{
		requests: map[string]ports.StoredInstallationRequest{"uninstall-once": stored},
		byID: map[string]domain.Installation{testInstall: {
			ID: testInstall, OwnerUserID: testOwner, ProjectID: testProject, AppID: "board-app",
			Version: "1.0.0", ManifestDigest: digestOf('g'), InstalledAt: tombstone.Add(-time.Hour),
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
