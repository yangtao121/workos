package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appregistryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	appregistrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

const (
	resolveOwner      = "0198d7ea-2110-7c42-b659-c5e4d73bc337"
	resolveProject    = "0198d7ea-2110-7c42-b659-c5e4d73bc341"
	resolveInstance   = "0198d7ea-2110-7c42-b659-c5e4d73bc342"
	resolveArtifactID = "0198d7ea-2110-7c42-b659-c5e4d73bc343"
	manifestHex       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	artifactHex       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	manifestDigest    = "sha256:" + manifestHex
	artifactDigest    = "sha256:" + artifactHex
)

func activeInstallation(appID, version, digest string) projectdomain.Installation {
	return projectdomain.Installation{
		ID: resolveInstance, OwnerUserID: resolveOwner, ProjectID: resolveProject,
		AppID: appID, Version: version, ManifestDigest: digest,
		// Post-mutable-grants rows always carry an epoch; fixtures default to
		// the install-time epoch 1.
		GrantRevision: 1,
		InstalledAt:   time.Now().UTC(),
	}
}

type fakeInstallations struct {
	installation projectdomain.Installation
	err          error
}

func (f *fakeInstallations) ResolveActiveInstallation(context.Context, string, string, string) (projectdomain.Installation, error) {
	if f.err != nil {
		return projectdomain.Installation{}, f.err
	}
	return f.installation, nil
}

type fakeRegistry struct {
	resolution    appregistryapp.WebBundleResolution
	surfaceResult appregistryapp.SurfaceResolution
	surfaceErr    error
	err           error
}

func (f *fakeRegistry) ResolveWebBundle(context.Context, string, string, string) (appregistryapp.WebBundleResolution, error) {
	if f.err != nil {
		return appregistryapp.WebBundleResolution{}, f.err
	}
	return f.resolution, nil
}

func (f *fakeRegistry) ResolveSurfaceLaunch(context.Context, string, string, string) (appregistryapp.SurfaceResolution, error) {
	if f.surfaceErr != nil {
		return appregistryapp.SurfaceResolution{}, f.surfaceErr
	}
	return f.surfaceResult, nil
}

type fakeArtifacts struct {
	summary artifactapp.BundleSummary
	asset   artifactdomain.BundleFile
	err     error
	calls   int
}

func (f *fakeArtifacts) VerifyWebBundle(context.Context, string, string, string) (artifactapp.BundleSummary, error) {
	f.calls++
	if f.err != nil {
		return artifactapp.BundleSummary{}, f.err
	}
	return f.summary, nil
}

func (f *fakeArtifacts) ReadVerifiedWebBundleAsset(_ context.Context, _, _, _, path string) (artifactdomain.BundleFile, error) {
	if f.err != nil {
		return artifactdomain.BundleFile{}, f.err
	}
	if path == "" {
		return f.asset, nil
	}
	return artifactdomain.BundleFile{}, artifactdomain.ErrNotFound
}

func bundleResolution(digest string) appregistryapp.WebBundleResolution {
	return appregistryapp.WebBundleResolution{
		ManifestDigest: digest,
		Ref: appregistrydomain.WebBundleRef{
			ArtifactID: resolveArtifactID, ArtifactDigest: artifactDigest,
		},
	}
}

func newResolver(installations *fakeInstallations, registry *fakeRegistry, artifacts *fakeArtifacts) *SurfaceLaunchResolver {
	resolver, err := NewSurfaceLaunchResolver(installations, registry, artifacts)
	if err != nil {
		panic(err)
	}
	return resolver
}

func TestResolveWebBundleHappyPath(t *testing.T) {
	t.Parallel()
	current := activeInstallation("notes", "1.2.0", manifestDigest)
	current.GrantRevision = 4
	resolver := newResolver(
		&fakeInstallations{installation: current},
		&fakeRegistry{resolution: bundleResolution(manifestDigest)},
		&fakeArtifacts{summary: artifactapp.BundleSummary{Entrypoint: "index.html"}},
	)
	descriptor, err := resolver.ResolveWebBundle(context.Background(), resolveOwner, resolveProject, resolveInstance)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if descriptor.AppID != "notes" || descriptor.Version != "1.2.0" ||
		descriptor.ManifestDigest != manifestDigest || descriptor.ArtifactID != resolveArtifactID ||
		descriptor.ArtifactDigest != artifactDigest || descriptor.Entrypoint != "index.html" {
		t.Fatalf("unexpected descriptor: %+v", descriptor)
	}
	// The authoritative grant epoch rides along with the grant set so the
	// runtime can persist both into the surface session.
	if descriptor.GrantRevision != 4 {
		t.Fatalf("descriptor must carry the installation grant revision, got %d", descriptor.GrantRevision)
	}
}

func TestResolveWebBundleFailsClosed(t *testing.T) {
	t.Parallel()
	happy := func() (*fakeInstallations, *fakeRegistry, *fakeArtifacts) {
		return &fakeInstallations{installation: activeInstallation("notes", "1.2.0", manifestDigest)},
			&fakeRegistry{resolution: bundleResolution(manifestDigest)},
			&fakeArtifacts{summary: artifactapp.BundleSummary{Entrypoint: "index.html"}}
	}
	cases := map[string]func(installations *fakeInstallations, registry *fakeRegistry, artifacts *fakeArtifacts) error{
		"foreign installation": func(i *fakeInstallations, _ *fakeRegistry, _ *fakeArtifacts) error {
			i.err = projectdomain.ErrNotFound
			_, err := newResolver(i, &fakeRegistry{}, &fakeArtifacts{}).ResolveWebBundle(context.Background(), resolveOwner, resolveProject, resolveInstance)
			return err
		},
		"unknown version": func(_ *fakeInstallations, r *fakeRegistry, _ *fakeArtifacts) error {
			r.err = appregistrydomain.ErrNotFound
			_, err := newResolver(&fakeInstallations{installation: activeInstallation("notes", "9.9.9", manifestDigest)}, r, &fakeArtifacts{}).ResolveWebBundle(context.Background(), resolveOwner, resolveProject, resolveInstance)
			return err
		},
		"unsupported runtime": func(_ *fakeInstallations, r *fakeRegistry, _ *fakeArtifacts) error {
			r.err = appregistryapp.ErrUnsupportedRuntime
			_, err := newResolver(&fakeInstallations{installation: activeInstallation("legacy", "1.0.0", manifestDigest)}, r, &fakeArtifacts{}).ResolveWebBundle(context.Background(), resolveOwner, resolveProject, resolveInstance)
			return err
		},
		"manifest digest drift": func(_ *fakeInstallations, r *fakeRegistry, _ *fakeArtifacts) error {
			r.resolution = bundleResolution("sha256:" + strings.Repeat("c", 64))
			_, err := newResolver(&fakeInstallations{installation: activeInstallation("notes", "1.2.0", manifestDigest)}, r, &fakeArtifacts{}).ResolveWebBundle(context.Background(), resolveOwner, resolveProject, resolveInstance)
			return err
		},
		"foreign artifact": func(_ *fakeInstallations, _ *fakeRegistry, a *fakeArtifacts) error {
			a.err = artifactdomain.ErrNotFound
			_, err := newResolver(&fakeInstallations{installation: activeInstallation("notes", "1.2.0", manifestDigest)}, &fakeRegistry{resolution: bundleResolution(manifestDigest)}, a).ResolveWebBundle(context.Background(), resolveOwner, resolveProject, resolveInstance)
			return err
		},
		"artifact digest drift": func(_ *fakeInstallations, _ *fakeRegistry, a *fakeArtifacts) error {
			a.err = artifactdomain.ErrDigestMismatch
			_, err := newResolver(&fakeInstallations{installation: activeInstallation("notes", "1.2.0", manifestDigest)}, &fakeRegistry{resolution: bundleResolution(manifestDigest)}, a).ResolveWebBundle(context.Background(), resolveOwner, resolveProject, resolveInstance)
			return err
		},
	}
	for name, mutate := range cases {
		i, r, a := happy()
		err := mutate(i, r, a)
		switch name {
		case "foreign installation", "unknown version", "foreign artifact":
			if !errors.Is(err, projectdomain.ErrNotFound) && !errors.Is(err, appregistrydomain.ErrNotFound) && !errors.Is(err, artifactdomain.ErrNotFound) {
				t.Errorf("%s: expected sanitized NotFound, got %v", name, err)
			}
		case "unsupported runtime":
			if !errors.Is(err, ErrLaunchUnsupported) {
				t.Errorf("%s: expected unsupported verdict, got %v", name, err)
			}
		case "manifest digest drift", "artifact digest drift":
			if !IsLaunchCorrupt(err) {
				t.Errorf("%s: expected sanitized corrupt verdict, got %v", name, err)
			}
		}
	}
}

func TestReadWebBundleAssetUsesPinnedDescriptorOnly(t *testing.T) {
	t.Parallel()
	entry := artifactdomain.BundleFile{Path: "index.html", MediaType: "text/html; charset=utf-8", Content: []byte("<p>ok</p>"), FileDigest: "sha256:" + strings.Repeat("d", 64)}
	resolver := newResolver(
		&fakeInstallations{installation: activeInstallation("notes", "1.2.0", manifestDigest)},
		&fakeRegistry{resolution: bundleResolution(manifestDigest)},
		&fakeArtifacts{summary: artifactapp.BundleSummary{Entrypoint: "index.html"}, asset: entry},
	)
	file, err := resolver.ReadWebBundleAsset(context.Background(), resolveOwner, resolveProject, resolveInstance, "")
	if err != nil || string(file.Content) != "<p>ok</p>" {
		t.Fatalf("entrypoint read failed: %v", err)
	}
	// A resolution failure never reaches storage reads.
	failing := newResolver(
		&fakeInstallations{err: projectdomain.ErrNotFound},
		&fakeRegistry{}, &fakeArtifacts{},
	)
	if _, err := failing.ReadWebBundleAsset(context.Background(), resolveOwner, resolveProject, resolveInstance, ""); !errors.Is(err, projectdomain.ErrNotFound) {
		t.Fatalf("denied read verdict: %v", err)
	}
}

func TestArtifactDirectoryAdaptsVerdicts(t *testing.T) {
	t.Parallel()
	directory, err := NewArtifactDirectory(&fakeArtifacts{summary: artifactapp.BundleSummary{Entrypoint: "index.html"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.VerifyWebBundle(context.Background(), resolveOwner, resolveArtifactID, artifactDigest); err != nil {
		t.Fatalf("verified reference denied: %v", err)
	}
	denied, err := NewArtifactDirectory(&fakeArtifacts{err: artifactdomain.ErrNotFound})
	if err != nil {
		t.Fatal(err)
	}
	if err := denied.VerifyWebBundle(context.Background(), resolveOwner, resolveArtifactID, artifactDigest); !errors.Is(err, appregistryapp.ErrArtifactDenied) {
		t.Fatalf("foreign reference verdict: %v", err)
	}
	mismatch, err := NewArtifactDirectory(&fakeArtifacts{err: artifactdomain.ErrDigestMismatch})
	if err != nil {
		t.Fatal(err)
	}
	if err := mismatch.VerifyWebBundle(context.Background(), resolveOwner, resolveArtifactID, artifactDigest); !errors.Is(err, appregistryapp.ErrArtifactDenied) {
		t.Fatalf("mismatch verdict: %v", err)
	}
	if _, err := NewArtifactDirectory(nil); err == nil {
		t.Fatal("missing artifact service accepted")
	}
}

func TestNewSurfaceLaunchResolverRequiresAllSources(t *testing.T) {
	t.Parallel()
	if _, err := NewSurfaceLaunchResolver(nil, &fakeRegistry{}, &fakeArtifacts{}); err == nil {
		t.Fatal("missing installations accepted")
	}
	if _, err := NewSurfaceLaunchResolver(&fakeInstallations{}, nil, &fakeArtifacts{}); err == nil {
		t.Fatal("missing registry accepted")
	}
	if _, err := NewSurfaceLaunchResolver(&fakeInstallations{}, &fakeRegistry{}, nil); err == nil {
		t.Fatal("missing artifacts accepted")
	}
}

// TestResolveSurfaceLaunchContainerPath pins the generic resolution: a
// container manifest resolves to the neutral container descriptor with the
// grant facts riding along; digest drift and unsupported runtimes keep their
// sanitized verdicts.
func TestResolveSurfaceLaunchContainerPath(t *testing.T) {
	installations := &fakeInstallations{installation: activeInstallation("container-app", "1.0.0", manifestDigest)}
	registry := &fakeRegistry{surfaceResult: appregistryapp.SurfaceResolution{
		ManifestDigest: manifestDigest,
		Container: &appregistrydomain.ContainerLaunch{
			Image:   "localhost/workos-fixture@sha256:" + manifestHex,
			Command: []string{"/workos-fixture", "serve"}, Port: 8080,
			Resources: appregistrydomain.ContainerResourcePolicy{CPUHardCores: 1, MemoryHighMB: 64, MemoryMaxMB: 96, PidsMax: 32},
			Health:    appregistrydomain.ContainerHealthPolicy{HTTPPath: "/health", StartupSeconds: 10, RestartLimit: 2},
		},
	}}
	resolver := newResolver(installations, registry, &fakeArtifacts{})

	launch, err := resolver.ResolveSurfaceLaunch(context.Background(), resolveOwner, resolveProject, resolveInstance)
	if err != nil {
		t.Fatalf("ResolveSurfaceLaunch: %v", err)
	}
	if launch.Kind != LaunchKindWebServiceContainer || launch.Container == nil || launch.WebBundle != nil {
		t.Fatalf("launch kind %+v, want container descriptor", launch.Kind)
	}
	if launch.Container.Image != "localhost/workos-fixture@sha256:"+manifestHex || launch.Container.Port != 8080 ||
		launch.Container.Route != "/" {
		t.Fatalf("container descriptor incomplete: %+v", launch.Container)
	}
	if launch.GrantRevision != 1 || len(launch.GrantedPermissions) != 0 {
		t.Fatalf("grant facts missing: %+v", launch)
	}

	// Digest drift between the installation pin and the registry version is
	// internal corruption, never a launch.
	registry.surfaceResult.ManifestDigest = "sha256:" + strings.Repeat("f", 64)
	_, err = resolver.ResolveSurfaceLaunch(context.Background(), resolveOwner, resolveProject, resolveInstance)
	if !IsLaunchCorrupt(err) {
		t.Fatalf("drift verdict %v, want corrupt", err)
	}

	// An unsupported runtime is FailedPrecondition, decided without any
	// artifact call.
	registry.surfaceErr = appregistryapp.ErrUnsupportedRuntime
	registry.surfaceResult = appregistryapp.SurfaceResolution{}
	before := resolver.artifacts.(*fakeArtifacts).calls
	if _, err := resolver.ResolveSurfaceLaunch(context.Background(), resolveOwner, resolveProject, resolveInstance); !errors.Is(err, ErrLaunchUnsupported) {
		t.Fatalf("unsupported runtime verdict %v", err)
	}
	if resolver.artifacts.(*fakeArtifacts).calls != before {
		t.Fatalf("unsupported runtime touched the artifact service")
	}
}
