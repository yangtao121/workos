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

// setGrantsFixture builds the canonical mutable-grants scene: one active
// board-app installation pinned to 1.2.0 with grant {agent.task.run} at epoch
// 1 under Project revision 4, and a catalog whose exact pinned version
// requested {agent.event.watch, agent.task.run, artifact.read}.
func setGrantsFixture() (*fakeRepository, *fakeCatalog) {
	repo := &fakeRepository{
		requests: map[string]ports.StoredInstallationRequest{},
		byID: map[string]domain.Installation{
			testInstall: {
				ID: testInstall, OwnerUserID: testOwner, ProjectID: testProject,
				AppID: "board-app", Version: "1.2.0", ManifestDigest: digestOf('a'),
				GrantedPermissions: []string{"agent.task.run"},
				GrantRevision:      1,
				InstalledAt:        time.Now().UTC(),
			},
		},
		projectRevision: 4,
	}
	pinned := pinned("1.2.0", digestOf('a'), "user")
	pinned.Permissions = []string{"agent.event.watch", "agent.task.run", "artifact.read"}
	catalog := &fakeCatalog{resolved: map[string]domain.PinnedApp{"board-app@1.2.0": pinned}}
	return repo, catalog
}

func setGrantsInput(key string, granted ...string) SetAppGrantsInput {
	return SetAppGrantsInput{
		OwnerUserID: testOwner, IdempotencyKey: key, ProjectID: testProject,
		InstallationID: testInstall, ExpectedRevision: 4, GrantedPermissions: granted,
	}
}

func TestSetAppGrantsRejectsMalformedRequests(t *testing.T) {
	t.Parallel()
	repo, catalog := setGrantsFixture()
	service := newInstallationService(t, repo, catalog)
	cases := map[string]SetAppGrantsInput{
		"missing owner":        {OwnerUserID: "", IdempotencyKey: "k", ProjectID: testProject, InstallationID: testInstall, ExpectedRevision: 1},
		"missing key":          setGrantsInput(""),
		"control-char key":     setGrantsInput("k\n"),
		"malformed project":    {OwnerUserID: testOwner, IdempotencyKey: "k", ProjectID: "not-a-uuid", InstallationID: testInstall, ExpectedRevision: 1},
		"malformed install id": {OwnerUserID: testOwner, IdempotencyKey: "k", ProjectID: testProject, InstallationID: "nope", ExpectedRevision: 1},
		"zero revision":        {OwnerUserID: testOwner, IdempotencyKey: "k", ProjectID: testProject, InstallationID: testInstall, ExpectedRevision: 0},
	}
	for name, input := range cases {
		if _, err := service.SetAppGrants(context.Background(), input); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: must be InvalidArgument, got %v", name, err)
		}
	}
	// Grant shape failures keep their dedicated sanitized verdict.
	for name, granted := range map[string][]string{
		"duplicate capability": {"agent.task.run", "agent.task.run"},
		"control character":    {"agent.task.run\x1b"},
		"invalid grammar":      {"Agent.Task.Run"},
		"empty capability":     {"agent.task.run", ""},
	} {
		input := setGrantsInput("shape", granted...)
		if _, err := service.SetAppGrants(context.Background(), input); !errors.Is(err, domain.ErrInvalidGrant) {
			t.Errorf("%s: must be ErrInvalidGrant, got %v", name, err)
		}
	}
	if len(repo.setLog) != 0 || catalog.calls != 0 {
		t.Fatal("malformed requests must not reach the catalog or repository")
	}
}

func TestSetAppGrantsRealChangeMovesBothRevisionsAndEmitsOnce(t *testing.T) {
	t.Parallel()
	repo, catalog := setGrantsFixture()
	service := newInstallationService(t, repo, catalog)
	// Client order is irrelevant; the command must carry the canonical sort.
	result, err := service.SetAppGrants(context.Background(), setGrantsInput("set-once", "artifact.read", "agent.task.run"))
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.setLog) != 1 {
		t.Fatalf("exactly one repository command expected, got %d", len(repo.setLog))
	}
	command := repo.setLog[0]
	if !equalCanonicalGrants(command.GrantedPermissions, []string{"agent.task.run", "artifact.read"}) {
		t.Fatalf("command grant not canonical: %v", command.GrantedPermissions)
	}
	if command.RequestDigest != domain.SetGrantsRequestDigest(testProject, testInstall, 4, []string{"agent.task.run", "artifact.read"}) {
		t.Fatal("repository must receive the canonical client digest")
	}
	if command.Pinned.Version != "1.2.0" || command.Pinned.ManifestDigest != digestOf('a') {
		t.Fatalf("command must pin the exact installation version: %#v", command.Pinned)
	}
	// Grant revision and Project revision each advance by exactly one.
	if result.Installation.GrantRevision != 2 {
		t.Fatalf("grant revision must be 2, got %d", result.Installation.GrantRevision)
	}
	if result.ProjectRevision != 5 || repo.projectRevision != 5 {
		t.Fatalf("project revision must advance by one, got %d/%d", result.ProjectRevision, repo.projectRevision)
	}
	if !equalCanonicalGrants(result.Installation.GrantedPermissions, []string{"agent.task.run", "artifact.read"}) {
		t.Fatalf("result grant not canonical: %v", result.Installation.GrantedPermissions)
	}
	// Exactly one event and one outbox row in the same simulated commit.
	if len(repo.events) != 1 || repo.events[0] != "project.app.grants.updated.v1" {
		t.Fatalf("exactly one grants-updated event expected: %v", repo.events)
	}
	if len(repo.outbox) != 1 || repo.outbox[0] != "project.app.grants.updated.v1" {
		t.Fatalf("exactly one outbox row expected: %v", repo.outbox)
	}
}

func TestSetAppGrantsEmptyTargetRevokesAll(t *testing.T) {
	t.Parallel()
	repo, catalog := setGrantsFixture()
	service := newInstallationService(t, repo, catalog)
	result, err := service.SetAppGrants(context.Background(), setGrantsInput("revoke-all"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Installation.GrantedPermissions) != 0 {
		t.Fatalf("empty target must revoke everything, got %v", result.Installation.GrantedPermissions)
	}
	if result.Installation.GrantRevision != 2 || result.ProjectRevision != 5 {
		t.Fatalf("revoke-all is a real change: revision=%d project=%d", result.Installation.GrantRevision, result.ProjectRevision)
	}
}

func TestSetAppGrantsRequiresExactPinnedSubset(t *testing.T) {
	t.Parallel()
	repo, catalog := setGrantsFixture()
	service := newInstallationService(t, repo, catalog)
	// A capability the pinned manifest never requested is a hard denial, and
	// the repository is never reached (no partial state, no consumed key).
	if _, err := service.SetAppGrants(context.Background(), setGrantsInput("over-grant", "knowledge.read")); !errors.Is(err, domain.ErrGrantNotRequested) {
		t.Fatalf("unrequested capability verdict: %v", err)
	}
	if len(repo.setLog) != 0 || len(repo.requests) != 0 {
		t.Fatal("denied set must not reach the repository or consume the key")
	}
}

func TestSetAppGrantsResolvesExactPinnedVersionOnly(t *testing.T) {
	t.Parallel()
	repo, catalog := setGrantsFixture()
	service := newInstallationService(t, repo, catalog)
	// A catalog that only knows the registry "current" (2.0.0) must never be
	// consulted with it: the command resolves the installation's pinned 1.2.0.
	pinnedV2 := pinned("2.0.0", digestOf('z'), "user")
	pinnedV2.Permissions = []string{"knowledge.read"}
	catalog.resolved["board-app@2.0.0"] = pinnedV2
	if _, err := service.SetAppGrants(context.Background(), setGrantsInput("pinned", "agent.task.run")); err != nil {
		t.Fatal(err)
	}
	if catalog.calls != 1 {
		t.Fatalf("exactly one exact-version resolution expected, got %d", catalog.calls)
	}
}

func TestSetAppGrantsReplaysConsumedKeyBeforeCatalog(t *testing.T) {
	t.Parallel()
	repo, catalog := setGrantsFixture()
	// The key was consumed by a first successful set whose response carried
	// grant {artifact.read} at epoch 2 under revision 5; the row has since
	// been mutated again.
	first := domain.SetGrantsRequestDigest(testProject, testInstall, 4, []string{"artifact.read"})
	repo.requests["set-once"] = ports.StoredInstallationRequest{
		Command: "set-grants", RequestDigest: first, InstallationID: testInstall,
		ProjectRevision: 5, ResultGrantedPermissions: []string{"artifact.read"}, ResultGrantRevision: 2,
	}
	current := repo.byID[testInstall]
	current.GrantedPermissions = []string{"agent.event.watch"}
	current.GrantRevision = 9
	repo.byID[testInstall] = current
	service := newInstallationService(t, repo, catalog)
	result, err := service.SetAppGrants(context.Background(), setGrantsInput("set-once", "artifact.read"))
	if err != nil {
		t.Fatal(err)
	}
	if !equalCanonicalGrants(result.Installation.GrantedPermissions, []string{"artifact.read"}) || result.Installation.GrantRevision != 2 {
		t.Fatalf("replay must return the first-response grant snapshot, got %#v", result.Installation)
	}
	if result.ProjectRevision != 5 {
		t.Fatalf("replay must return the first-response project revision, got %d", result.ProjectRevision)
	}
	if catalog.calls != 0 || len(repo.setLog) != 0 {
		t.Fatal("replay must happen before any catalog resolution or repository command")
	}
}

func TestSetAppGrantsConsumedKeyDifferentDigestAborts(t *testing.T) {
	t.Parallel()
	repo, catalog := setGrantsFixture()
	repo.requests["set-once"] = ports.StoredInstallationRequest{
		Command:                  "set-grants",
		RequestDigest:            domain.SetGrantsRequestDigest(testProject, testInstall, 4, []string{"agent.task.run"}),
		InstallationID:           testInstall,
		ProjectRevision:          5,
		ResultGrantedPermissions: []string{"agent.task.run"}, ResultGrantRevision: 2,
	}
	service := newInstallationService(t, repo, catalog)
	if _, err := service.SetAppGrants(context.Background(), setGrantsInput("set-once", "artifact.read")); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same key different digest must be Aborted, got %v", err)
	}
	if catalog.calls != 0 || len(repo.setLog) != 0 {
		t.Fatal("conflict must be decided before catalog or repository work")
	}
}

func TestSetAppGrantsNoOpConsumesKeyWithoutSideEffects(t *testing.T) {
	t.Parallel()
	repo, catalog := setGrantsFixture()
	service := newInstallationService(t, repo, catalog)
	// The target equals the current canonical grant: deterministic no-op.
	result, err := service.SetAppGrants(context.Background(), setGrantsInput("noop", "agent.task.run"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Installation.GrantRevision != 1 || result.ProjectRevision != 4 {
		t.Fatalf("no-op must not move either revision: grant=%d project=%d", result.Installation.GrantRevision, result.ProjectRevision)
	}
	if len(repo.events) != 0 || len(repo.outbox) != 0 {
		t.Fatal("no-op must not emit events or outbox rows")
	}
	stored, found := repo.requests["noop"]
	if !found {
		t.Fatal("successful no-op must still durably consume the key")
	}
	if stored.ProjectRevision != 4 || stored.ResultGrantRevision != 1 ||
		!equalCanonicalGrants(stored.ResultGrantedPermissions, []string{"agent.task.run"}) {
		t.Fatalf("no-op snapshot must pin the current facts: %#v", stored)
	}
	// The consumed key replays exactly, still without any side effect.
	replayed, err := service.SetAppGrants(context.Background(), setGrantsInput("noop", "agent.task.run"))
	if err != nil || replayed.ProjectRevision != 4 || replayed.Installation.GrantRevision != 1 {
		t.Fatalf("no-op replay must return the identical facts: %#v err=%v", replayed, err)
	}
	if len(repo.setLog) != 1 || len(repo.events) != 0 {
		t.Fatal("replay must not re-run the repository command or emit events")
	}
}

func TestSetAppGrantsFailureDoesNotConsumeKey(t *testing.T) {
	t.Parallel()
	repo, catalog := setGrantsFixture()
	repo.setFn = func(command ports.SetAppGrantsCommand) (ports.InstallationResult, error) {
		return ports.InstallationResult{}, errors.New("tx failed")
	}
	service := newInstallationService(t, repo, catalog)
	if _, err := service.SetAppGrants(context.Background(), setGrantsInput("fails", "artifact.read")); err == nil {
		t.Fatal("repository failure must surface")
	}
	if _, consumed := repo.requests["fails"]; consumed {
		t.Fatal("a failed command must not consume the key")
	}
	// The retry reaches the repository again instead of replaying a phantom.
	repo.setFn = nil
	if _, err := service.SetAppGrants(context.Background(), setGrantsInput("fails", "artifact.read")); err != nil {
		t.Fatal(err)
	}
	if len(repo.setLog) != 2 {
		t.Fatalf("retry must re-attempt the command, got %d calls", len(repo.setLog))
	}
}

func TestSetAppGrantsPinnedIdentityDriftFailsClosed(t *testing.T) {
	t.Parallel()
	repo, catalog := setGrantsFixture()
	service := newInstallationService(t, repo, catalog)
	// A catalog answer whose digest is well-formed but differs from the
	// pinned one is drift ('b' is valid hex, unlike the stored 'a').
	drifted := pinned("1.2.0", digestOf('b'), "user")
	drifted.Permissions = []string{"agent.task.run"}
	catalog.resolved["board-app@1.2.0"] = drifted
	if _, err := service.SetAppGrants(context.Background(), setGrantsInput("drift", "agent.task.run")); !errors.Is(err, errPinnedIdentityDrift) {
		t.Fatalf("manifest digest drift must be the sanitized internal drift verdict, got %v", err)
	}
	// A well-formed different version is drift as well.
	versioned := pinned("1.3.0", digestOf('a'), "user")
	versioned.Permissions = []string{"agent.task.run"}
	catalog.resolved["board-app@1.2.0"] = versioned
	if _, err := service.SetAppGrants(context.Background(), setGrantsInput("drift", "agent.task.run")); !errors.Is(err, errPinnedIdentityDrift) {
		t.Fatalf("version drift must be the sanitized internal drift verdict, got %v", err)
	}
	if len(repo.setLog) != 0 {
		t.Fatal("drift must never reach the repository")
	}
	// A scope the install path would have rejected is corruption here too.
	scoped := pinned("1.2.0", digestOf('a'), "system")
	scoped.Permissions = []string{"agent.task.run"}
	catalog.resolved["board-app@1.2.0"] = scoped
	if _, err := service.SetAppGrants(context.Background(), setGrantsInput("drift", "agent.task.run")); !errors.Is(err, errAppScopeViolated) {
		t.Fatalf("non-installable scope must fail closed, got %v", err)
	}
}

func TestSetAppGrantsUnavailableVerdicts(t *testing.T) {
	t.Parallel()
	// Catalog dependency outage surfaces through the neutral port as the
	// project dependency-unavailable sentinel.
	repo, catalog := setGrantsFixture()
	catalog.err = fmt.Errorf("resolve app for installation: %w", ports.ErrStoreUnavailable)
	service := newInstallationService(t, repo, catalog)
	if _, err := service.SetAppGrants(context.Background(), setGrantsInput("catalog-down", "agent.task.run")); !errors.Is(err, ports.ErrStoreUnavailable) {
		t.Fatalf("catalog outage must be Unavailable-class, got %v", err)
	}
	// Repository outage on the authority read is the same verdict class.
	repoDown, catalogUp := setGrantsFixture()
	repoDown.resolveErr = fmt.Errorf("resolve active installation: %w", ports.ErrStoreUnavailable)
	service = newInstallationService(t, repoDown, catalogUp)
	if _, err := service.SetAppGrants(context.Background(), setGrantsInput("repo-down", "agent.task.run")); !errors.Is(err, ports.ErrStoreUnavailable) {
		t.Fatalf("repository outage must be Unavailable-class, got %v", err)
	}
}

func TestSetAppGrantsUnknownInstallationIsNotFound(t *testing.T) {
	t.Parallel()
	repo, catalog := setGrantsFixture()
	delete(repo.byID, testInstall)
	service := newInstallationService(t, repo, catalog)
	if _, err := service.SetAppGrants(context.Background(), setGrantsInput("missing", "agent.task.run")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown installation verdict: %v", err)
	}
	if catalog.calls != 0 || len(repo.setLog) != 0 {
		t.Fatal("a miss must not resolve the catalog or run a command")
	}
}

// TestInstallReplayAfterGrantMutationReturnsFirstResponseSnapshot pins the
// result-snapshot overlay: a historical install key replayed after a later
// SetAppGrants must return the grant/epoch of the first install response,
// not the mutated row.
func TestInstallReplayAfterGrantMutationReturnsFirstResponseSnapshot(t *testing.T) {
	t.Parallel()
	repo, catalog := setGrantsFixture()
	repo.requests["install-1"] = ports.StoredInstallationRequest{
		Command:                  "install",
		RequestDigest:            domain.InstallationRequestDigestWithGrants("install", testProject, "board-app", "1.2.0", "", 4, []string{"agent.task.run"}),
		InstallationID:           testInstall,
		ProjectRevision:          5,
		ResultGrantedPermissions: []string{"agent.task.run"}, ResultGrantRevision: 1,
	}
	service := newInstallationService(t, repo, catalog)
	input := installInput("install-1")
	input.Version = "1.2.0"
	input.GrantedPermissions = []string{"agent.task.run"}
	result, err := service.Install(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !equalCanonicalGrants(result.Installation.GrantedPermissions, []string{"agent.task.run"}) || result.Installation.GrantRevision != 1 {
		t.Fatalf("install replay must return the first-response snapshot, got %#v", result.Installation)
	}
	if catalog.calls != 0 {
		t.Fatal("install replay must stay before catalog resolution")
	}
}
