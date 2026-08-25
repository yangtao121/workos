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

// registryRepoStub feeds a real App Registry application service so the
// catalog bridge is exercised against the service type the composition root
// wires, without any database.
type registryRepoStub struct {
	summary appregistrydomain.AppVersionSummary
	err     error
}

func (r registryRepoStub) Register(context.Context, appregistrydomain.AppVersion) (appregistrydomain.AppVersionSummary, error) {
	return appregistrydomain.AppVersionSummary{}, nil
}

func (r registryRepoStub) GetVersion(context.Context, string, string, string) (appregistrydomain.AppVersionSummary, error) {
	return r.summary, r.err
}

func (r registryRepoStub) ListAppIDPage(context.Context, string, string, int) ([]string, string, error) {
	return nil, "", nil
}

func (r registryRepoStub) VisitVersionSummaries(_ context.Context, _ string, _ []string, visit func(appregistrydomain.AppVersionSummary) error) error {
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
	catalog := newCatalog(t, registryRepoStub{summary: appregistrydomain.AppVersionSummary{
		AppID: "board-app", Version: "1.10.0", Scope: appregistrydomain.ScopeUser,
		Name: "Board", Permissions: []string{"artifact.read"}, ManifestDigest: "sha256:" + hex64('a'),
	}})
	pinned, err := catalog.Resolve(context.Background(), "owner-1", "board-app", "")
	if err != nil {
		t.Fatal(err)
	}
	if pinned.AppID != "board-app" || pinned.Version != "1.10.0" || pinned.ManifestDigest != "sha256:"+hex64('a') || pinned.Scope != "user" {
		t.Fatalf("unexpected pinned reference: %#v", pinned)
	}
	if pinned.Scope == "" {
		t.Fatal("scope must be projected for the fail-closed check")
	}
}

func TestAppCatalogExplicitVersionUsesImmutableRead(t *testing.T) {
	t.Parallel()
	var requestedVersion string
	repo := registryRepoStub{summary: appregistrydomain.AppVersionSummary{AppID: "board-app", Version: "1.9.0"}}
	catalog := newCatalog(t, repo)
	pinned, err := catalog.Resolve(context.Background(), "owner-1", "board-app", "1.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if requestedVersion != "" || pinned.Version != "1.9.0" {
		t.Fatalf("explicit version must pin the immutable read: %#v", pinned)
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
