package orchestration

import (
	"context"
	"errors"
	"testing"

	appregistryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	appregistrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

// catalogCall records what the App Registry repository actually received,
// so tests assert the arguments crossed the bridge instead of trusting the
// request to reach storage.
type catalogCall struct {
	ownerUserID string
	appID       string
	version     string
}

// registryRepoStub feeds a real App Registry application service so the
// catalog bridge is exercised against the service type the composition root
// wires, without any database. Calls are recorded on the shared calls slice.
type registryRepoStub struct {
	summary appregistrydomain.AppVersionSummary
	err     error
	// calls collects both the immutable GetVersion path and the current
	// fold's summary stream; tests read it after Resolve returns.
	calls *[]catalogCall
}

func (r registryRepoStub) record(ownerUserID, appID, version string) {
	if r.calls != nil {
		*r.calls = append(*r.calls, catalogCall{ownerUserID: ownerUserID, appID: appID, version: version})
	}
}

func (r registryRepoStub) Register(context.Context, appregistrydomain.AppVersion) (appregistrydomain.AppVersionSummary, error) {
	return appregistrydomain.AppVersionSummary{}, nil
}

func (r registryRepoStub) GetVersion(_ context.Context, ownerUserID, appID, version string) (appregistrydomain.AppVersionSummary, error) {
	r.record(ownerUserID, appID, version)
	return r.summary, r.err
}

func (r registryRepoStub) ListAppIDPage(context.Context, string, string, int) ([]string, string, error) {
	return nil, "", nil
}

func (r registryRepoStub) VisitVersionSummaries(_ context.Context, ownerUserID string, appIDs []string, visit func(appregistrydomain.AppVersionSummary) error) error {
	for _, appID := range appIDs {
		r.record(ownerUserID, appID, "")
	}
	if r.err != nil {
		return r.err
	}
	if r.summary.AppID == "" {
		// No versions registered: the stream stays empty.
		return nil
	}
	return visit(r.summary)
}

type voidValidator struct{}

func (voidValidator) Validate([]byte) (appregistrydomain.Manifest, []string) {
	return appregistrydomain.Manifest{}, nil
}

type staticGenerator struct{}

func (staticGenerator) New() string { return "01999999-9999-7999-8999-999999999994" }

func newCatalog(t *testing.T, repo registryRepoStub) *AppCatalog {
	t.Helper()
	service, err := appregistryapp.New(repo, voidValidator{}, nil, staticGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewAppCatalog(service)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestAppCatalogResolvesPinnedReference(t *testing.T) {
	t.Parallel()
	var calls []catalogCall
	catalog := newCatalog(t, registryRepoStub{
		summary: appregistrydomain.AppVersionSummary{
			AppID: "board-app", Version: "1.10.0", Scope: appregistrydomain.ScopeUser,
			Name: "Board", Permissions: []string{"artifact.read"}, ManifestDigest: "sha256:" + hex64('a'),
		},
		calls: &calls,
	})
	pinned, err := catalog.Resolve(context.Background(), "owner-1", "board-app", "")
	if err != nil {
		t.Fatal(err)
	}
	if pinned.AppID != "board-app" || pinned.Version != "1.10.0" || pinned.ManifestDigest != "sha256:"+hex64('a') || pinned.Scope != "user" {
		t.Fatalf("unexpected pinned reference: %#v", pinned)
	}
	// An empty requested version goes to the current fold's summary stream,
	// never to the immutable GetVersion path with a fabricated version.
	if len(calls) != 1 || calls[0] != (catalogCall{ownerUserID: "owner-1", appID: "board-app", version: ""}) {
		t.Fatalf("current resolution must stream summaries for the requested app only: %#v", calls)
	}
	if pinned.Scope == "" {
		t.Fatal("scope must be projected for the fail-closed check")
	}
}

func TestAppCatalogExplicitVersionUsesImmutableRead(t *testing.T) {
	t.Parallel()
	var calls []catalogCall
	repo := registryRepoStub{summary: appregistrydomain.AppVersionSummary{AppID: "board-app", Version: "1.9.0"}, calls: &calls}
	catalog := newCatalog(t, repo)
	pinned, err := catalog.Resolve(context.Background(), "owner-1", "board-app", "1.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Version != "1.9.0" {
		t.Fatalf("explicit version must pin the immutable read: %#v", pinned)
	}
	// The explicit version must reach the registry's immutable GetVersion
	// path verbatim, together with the owner and app id from the caller.
	if len(calls) != 1 {
		t.Fatalf("explicit resolution must make exactly one repository call, got %v", calls)
	}
	if calls[0] != (catalogCall{ownerUserID: "owner-1", appID: "board-app", version: "1.9.0"}) {
		t.Fatalf("explicit version was not forwarded verbatim: %#v", calls[0])
	}
}

func TestAppCatalogMapsRegistryDenials(t *testing.T) {
	t.Parallel()
	// Unknown app: the summary stream yields nothing, so the registry Get
	// itself reports NotFound and the catalog maps it to the installable
	// denial.
	notFound := newCatalog(t, registryRepoStub{})
	if _, err := notFound.Resolve(context.Background(), "owner-1", "missing-app", ""); !errors.Is(err, projectapp.ErrAppNotInstallable) {
		t.Fatalf("registry NotFound must map to the installable denial, got %v", err)
	}
	// Malformed version: the registry application rejects it before storage.
	if _, err := notFound.Resolve(context.Background(), "owner-1", "board-app", "01.2.3"); !errors.Is(err, projectdomain.ErrInvalid) {
		t.Fatalf("registry Invalid must stay InvalidArgument, got %v", err)
	}
	// Infrastructure failures stay distinguishable from denials.
	internal := newCatalog(t, registryRepoStub{err: errors.New("visit failed")})
	if _, err := internal.Resolve(context.Background(), "owner-1", "board-app", ""); err == nil || errors.Is(err, projectapp.ErrAppNotInstallable) {
		t.Fatalf("infrastructure failures must not become denials, got %v", err)
	}
}

func TestNewAppCatalogRequiresRegistry(t *testing.T) {
	t.Parallel()
	if _, err := NewAppCatalog(nil); err == nil {
		t.Fatal("catalog must fail closed without the registry service")
	}
}

func hex64(char rune) string {
	value := make([]rune, 64)
	for index := range value {
		value[index] = char
	}
	return string(value)
}
