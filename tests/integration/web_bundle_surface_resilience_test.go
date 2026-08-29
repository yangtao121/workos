//go:build integration

package integration_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/core/appregistry/adapters/manifestvalidator"
	appregistrypostgres "github.com/yangtao121/workos/internal/core/appregistry/adapters/postgres"
	appregistryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	artifactpostgres "github.com/yangtao121/workos/internal/core/artifact/adapters/postgres"
	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	"github.com/yangtao121/workos/internal/core/orchestration"
	orchestrationsurface "github.com/yangtao121/workos/internal/core/orchestration/transport"
	projectpostgres "github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	surfacecoreclient "github.com/yangtao121/workos/internal/runtime/surface/adapters/coreclient"
	surfacepostgres "github.com/yangtao121/workos/internal/runtime/surface/adapters/postgres"
	surfaceapp "github.com/yangtao121/workos/internal/runtime/surface/application"
	surfaceports "github.com/yangtao121/workos/internal/runtime/surface/ports"
	surfacetransport "github.com/yangtao121/workos/internal/runtime/surface/transport"
)

const resiliencyOwner = "0198d7ea-2110-7c42-b659-c5e4d73bc337"
const resiliencyDevice = "0198d7ea-2110-7c42-b659-c5e4d73bc338"

// newUnreachablePool opens a real pgx pool pointed at a real closed loopback
// port: every query fails with a genuine TCP connection refusal, which is
// the adapter-level equivalent of the owned database process being down.
func newUnreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://workos:workos@127.0.0.1:1/unreachable?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// silentResolver guards the resolution paths that must never be reached
// while the local session store is down.
type silentResolver struct{}

func (silentResolver) ResolveWebBundle(context.Context, surfaceports.ResolveQuery) (surfaceports.LaunchDescriptor, error) {
	panic("resolver must not be reached while the session store is down")
}

func (silentResolver) ReadWebBundleAsset(context.Context, surfaceports.AssetQuery) (surfaceports.Asset, error) {
	panic("resolver must not be reached while the session store is down")
}

// mustRuntimeService wires the real runtime surface application over the
// given (real) repository with the given resolver port.
func mustRuntimeService(t *testing.T, pool *pgxpool.Pool, resolver surfaceports.LaunchResolver) *surfaceapp.Service {
	t.Helper()
	service, err := surfaceapp.New(surfacepostgres.New(pool), resolver, ids.UUIDv7{}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func identityCreateRequest(key string) *connect.Request[surfacev1.CreateSurfaceRequest] {
	request := connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey: key,
		ProjectId:      newUUIDForTest(111),
		AppInstanceId:  newUUIDForTest(112),
		DeviceClass:    surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:       &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
	})
	request.Header().Set(identity.UserHeader, resiliencyOwner)
	request.Header().Set(identity.DeviceHeader, resiliencyDevice)
	return request
}

// TestRuntimeStoreOutageIsUnavailableNotMissing proves the Runtime DB path
// with the real PostgreSQL adapter against a genuinely unreachable server:
// CreateSurface must be a sanitized Unavailable (never NotFound, never
// Internal) and the error must not leak the failed address.
func TestRuntimeStoreOutageIsUnavailableNotMissing(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := newUnreachablePool(t)
	_, publicHandler := surfacetransport.NewConnectHandler(mustRuntimeService(t, pool, silentResolver{}))
	server := httptest.NewServer(identity.Middleware(publicHandler))
	t.Cleanup(server.Close)
	client := surfacev1connect.NewSurfaceServiceClient(server.Client(), server.URL)

	_, err := client.CreateSurface(ctx, identityCreateRequest("outage-key"))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("runtime store outage verdict %v, want Unavailable", err)
	}
	if got := err.Error(); strings.Contains(got, "sql") || strings.Contains(got, "127.0.0.1:1") {
		t.Fatalf("unsanitized outage error: %s", got)
	}
}

// TestCoreResolverDependencyOutageExposesUnavailable proves the private Core
// dependency path end to end with only real adapters: the real project
// PostgreSQL repository (unreachable server) behind the real private
// resolver transport, reached by the real runtime Core client, surfaced by
// the real public SurfaceService handler as Unavailable.
func TestCoreResolverDependencyOutageExposesUnavailable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := newUnreachablePool(t)
	projects := projectpostgres.New(pool)
	projectsService := projectapp.New(projects, ids.UUIDv7{})
	artifactsService, err := artifactapp.New(artifactpostgres.New(pool), ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := manifestvalidator.New()
	if err != nil {
		t.Fatal(err)
	}
	artifactDirectory, err := orchestration.NewArtifactDirectory(artifactsService)
	if err != nil {
		t.Fatal(err)
	}
	projectDirectory, err := orchestration.NewProjectDirectory(projectsService)
	if err != nil {
		t.Fatal(err)
	}
	registryService, err := appregistryapp.New(appregistrypostgres.New(pool), validator, projectDirectory, artifactDirectory, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	launchResolver, err := orchestration.NewSurfaceLaunchResolver(projects, registryService, artifactsService)
	if err != nil {
		t.Fatal(err)
	}
	_, privateHandler := orchestrationsurface.NewSurfaceResolverConnectHandler(launchResolver)
	coreServer := httptest.NewServer(identity.Middleware(privateHandler))
	t.Cleanup(coreServer.Close)

	coreClient, err := surfacecoreclient.New(
		surfacev1connect.NewSurfaceLaunchResolverServiceClient(
			&http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}},
			coreServer.URL,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, publicHandler := surfacetransport.NewConnectHandler(mustRuntimeService(t, pool, coreClient))
	publicServer := httptest.NewServer(identity.Middleware(publicHandler))
	t.Cleanup(publicServer.Close)
	client := surfacev1connect.NewSurfaceServiceClient(publicServer.Client(), publicServer.URL)

	_, err = client.CreateSurface(ctx, identityCreateRequest("core-outage-key"))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("core dependency outage verdict %v, want Unavailable", err)
	}
	if got := err.Error(); strings.Contains(got, "sql") || strings.Contains(got, "127.0.0.1:1") {
		t.Fatalf("unsanitized outage error: %s", got)
	}
}

// TestRuntimeAssetOutageServes503 proves the same outage on the asset route:
// the real HTTP handler must answer the sanitized 503 — never a "missing"
// 404 — and the outage page keeps the server-enforced CSP sandbox.
func TestRuntimeAssetOutageServes503(t *testing.T) {
	t.Parallel()
	pool := newUnreachablePool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	assetHandler := identity.Middleware(surfacetransport.NewAssetHandler(mustRuntimeService(t, pool, silentResolver{}), logger))
	server := httptest.NewServer(assetHandler)
	t.Cleanup(server.Close)

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/", nil)
	request.Header.Set(identity.UserHeader, resiliencyOwner)
	request.Header.Set(identity.DeviceHeader, resiliencyDevice)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close() //nolint:errcheck
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("asset outage status %d, want 503", response.StatusCode)
	}
	if csp := response.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox allow-scripts") {
		t.Fatalf("outage response lost the sandbox directive: %q", csp)
	}
}

func (silentResolver) ResolveSurfaceLaunch(context.Context, surfaceports.ResolveQuery) (surfaceports.ResolvedLaunch, error) {
	// The outage tests wire web-bundle paths only; a silent resolver stays
	// equally unreachable on the generic resolution path.
	return surfaceports.ResolvedLaunch{}, surfaceports.ErrResolverUnavailable
}
