//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"connectrpc.com/connect"

	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	artifactv1connect "github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
)

func artifactClients(t *testing.T) artifactv1connect.ArtifactServiceClient {
	t.Helper()
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	baseURL := os.Getenv("WORKOS_TEST_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	return artifactv1connect.NewArtifactServiceClient(httpClient, baseURL)
}

func surfaceClients(t *testing.T) surfacev1connect.SurfaceServiceClient {
	t.Helper()
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	baseURL := os.Getenv("WORKOS_TEST_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	return surfacev1connect.NewSurfaceServiceClient(httpClient, baseURL)
}

// directRuntimeClients reaches runtime-host directly (bypassing the gateway),
// so a test can present two different trusted device identities the way the
// gateway would inject them for two paired devices.
func directRuntimeClients(t *testing.T, userID, deviceID string) surfacev1connect.SurfaceServiceClient {
	t.Helper()
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	baseURL := os.Getenv("WORKOS_TEST_RUNTIME_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8083"
	}
	return surfacev1connect.NewSurfaceServiceClient(
		&identityHTTPClient{client: httpClient, userID: userID, deviceID: deviceID},
		baseURL,
	)
}

// identityHTTPClient stamps the trusted identity headers on every request,
// the same contract the gateway director applies in production.
type identityHTTPClient struct {
	client   *http.Client
	userID   string
	deviceID string
}

func (i *identityHTTPClient) Do(request *http.Request) (*http.Response, error) {
	request.Header.Set("X-WorkOS-User-ID", i.userID)
	request.Header.Set("X-WorkOS-Device-ID", i.deviceID)
	return i.client.Do(request)
}

func gatewayBaseURL() string {
	baseURL := os.Getenv("WORKOS_TEST_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	return baseURL
}

func bundleFiles() []*artifactv1.WebBundleFile {
	return []*artifactv1.WebBundleFile{
		{Path: "index.html", Content: []byte("<!doctype html><title>Bundle</title><div id=\"root\"></div><script src=\"app.js\"></script>")},
		{Path: "app.js", Content: []byte("document.getElementById('root').textContent = 'surface-ok';")},
	}
}

func createArtifact(t *testing.T, ctx context.Context, client artifactv1connect.ArtifactServiceClient, key, title string, files []*artifactv1.WebBundleFile, entrypoint string) *artifactv1.Artifact {
	t.Helper()
	response, err := client.CreateArtifact(ctx, connect.NewRequest(&artifactv1.CreateArtifactRequest{
		IdempotencyKey: key,
		Artifact:       &artifactv1.Artifact{Title: title},
		WebBundle:      &artifactv1.WebBundleContent{Entrypoint: entrypoint, Files: files},
	}))
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	return response.Msg.GetArtifact()
}

func bundleManifest(appID, name, version, artifactID, artifactDigest string) []byte {
	return []byte(fmt.Sprintf(`apiVersion: workos.app/v1
id: %s
name: %s
version: %s
scope: user
runtime:
  type: web-bundle
  artifactId: %s
  artifactDigest: %s
surfaces:
  - id: main
    renderer: web-bundle
    route: /
    adaptive: true
permissions: [artifact.read]
resources: {}
health: {}
maintainer: {}
`, appID, name, version, artifactID, artifactDigest))
}

func registerBundleApp(t *testing.T, ctx context.Context, appID, name, version, artifactID, artifactDigest string) string {
	t.Helper()
	response, err := appRegistryClients(t).RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: fmt.Sprintf("register-bundle-%s-%s-%d", appID, version, time.Now().UnixNano()),
		ManifestYaml:   bundleManifest(appID, name, version, artifactID, artifactDigest),
	}))
	if err != nil {
		t.Fatalf("register bundle app %s@%s: %v", appID, version, err)
	}
	return response.Msg.GetApp().GetManifestDigest()
}

func httpGet(t *testing.T, path string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, gatewayBaseURL()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { io.Copy(io.Discard, response.Body); response.Body.Close() }) //nolint:errcheck
	return response
}

// TestWebBundleSurfaceVerticalSlice proves the full installed-instance chain
// through the real gateway, Core, runtime-host, and PostgreSQL: artifact →
// manifest → installation → surface session → authenticated static assets →
// revocation.
func TestWebBundleSurfaceVerticalSlice(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	artifacts := artifactClients(t)
	surfaces := surfaceClients(t)
	projects := integrationProjectClients(t)
	installations := installationClients(t)

	stamp := time.Now().UnixNano()
	appID := fmt.Sprintf("bundle-notes-%d", stamp)

	artifact := createArtifact(t, ctx, artifacts, fmt.Sprintf("bundle-create-%d", stamp), "Notes Bundle", bundleFiles(), "index.html")
	if artifact.GetType() != "app.web-bundle.v1" || artifact.GetFileCount() != 2 || artifact.GetTotalSizeBytes() == 0 {
		t.Fatalf("unexpected artifact metadata: %#v", artifact)
	}
	if artifact.GetId() == "" || artifact.GetContentRef() == "" || !strings.HasPrefix(artifact.GetDigest(), "sha256:") || artifact.GetCreatedAt() == nil {
		t.Fatalf("server-owned fields missing: %#v", artifact)
	}

	t.Run("ArtifactIdempotencyAndValidation", func(t *testing.T) {
		// Same key, same logical bundle in reversed submission order replays.
		reversed := []*artifactv1.WebBundleFile{bundleFiles()[1], bundleFiles()[0]}
		replayed := createArtifact(t, ctx, artifacts, fmt.Sprintf("bundle-create-%d", stamp), "Notes Bundle", reversed, "index.html")
		if replayed.GetId() != artifact.GetId() || replayed.GetDigest() != artifact.GetDigest() {
			t.Fatalf("order-independent replay returned a different artifact: %s vs %s", replayed.GetId(), artifact.GetId())
		}
		// Same key, different content conflicts.
		_, err := artifacts.CreateArtifact(ctx, connect.NewRequest(&artifactv1.CreateArtifactRequest{
			IdempotencyKey: fmt.Sprintf("bundle-create-%d", stamp),
			Artifact:       &artifactv1.Artifact{Title: "Notes Bundle"},
			WebBundle: &artifactv1.WebBundleContent{Entrypoint: "index.html", Files: []*artifactv1.WebBundleFile{
				{Path: "index.html", Content: []byte("different")},
			}},
		}))
		if connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("same-key conflict verdict: %v", err)
		}
		// Validation failures do not consume a fresh key.
		badKey := fmt.Sprintf("bundle-bad-%d", stamp)
		_, invalidErr := artifacts.CreateArtifact(ctx, connect.NewRequest(&artifactv1.CreateArtifactRequest{
			IdempotencyKey: badKey,
			Artifact:       &artifactv1.Artifact{Title: "Bad"},
			WebBundle:      &artifactv1.WebBundleContent{Entrypoint: "missing.html", Files: bundleFiles()},
		}))
		if connect.CodeOf(invalidErr) != connect.CodeInvalidArgument {
			t.Fatalf("invalid bundle verdict: %v", invalidErr)
		}
		if countRows(t, `SELECT count(*) FROM workos_core.web_bundle_artifact_requests WHERE idempotency_key = $1`, badKey) != 0 {
			t.Fatal("failed validation consumed the artifact idempotency key")
		}
		// Public metadata reads never include bytes and are owner-scoped.
		got, err := artifacts.GetArtifact(ctx, connect.NewRequest(&artifactv1.GetArtifactRequest{ArtifactId: artifact.GetId()}))
		if err != nil || got.Msg.GetArtifact().GetDigest() != artifact.GetDigest() {
			t.Fatalf("get artifact failed: %v", err)
		}
		// The persistent acceptance database accumulates artifacts for the
		// fixed gateway identity across runs, so membership must be proven by
		// following the paging cursor instead of assuming a single page.
		found := false
		token := ""
		for pages := 0; !found && pages < 20; pages++ {
			listed, err := artifacts.ListArtifacts(ctx, connect.NewRequest(&artifactv1.ListArtifactsRequest{
				Page: &commonv1.PageRequest{PageSize: 100, PageToken: token},
			}))
			if err != nil {
				t.Fatalf("list artifacts: %v", err)
			}
			for _, candidate := range listed.Msg.GetArtifacts() {
				if candidate.GetId() == artifact.GetId() {
					found = true
				}
			}
			token = listed.Msg.GetPage().GetNextPageToken()
			if token == "" {
				break
			}
		}
		if !found {
			t.Fatal("owner list is missing the created artifact")
		}
	})

	t.Run("RegistryVerifiesBundleReference", func(t *testing.T) {
		// A wrong digest is a sanitized NotFound, not an existence oracle.
		wrongDigest := "sha256:" + strings.Repeat("f", 64)
		_, err := appRegistryClients(t).RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: fmt.Sprintf("register-bundle-wrong-%d", stamp),
			ManifestYaml:   bundleManifest(appID+"-wrong", "Wrong", "1.0.0", artifact.GetId(), wrongDigest),
		}))
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("foreign digest verdict: %v", err)
		}
		// A foreign artifact id is likewise denied.
		foreignID := newUUIDForTest(21)
		_, foreignErr := appRegistryClients(t).RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: fmt.Sprintf("register-bundle-foreign-%d", stamp),
			ManifestYaml:   bundleManifest(appID+"-foreign", "Foreign", "1.0.0", foreignID, artifact.GetDigest()),
		}))
		if connect.CodeOf(foreignErr) != connect.CodeNotFound {
			t.Fatalf("foreign artifact verdict: %v", foreignErr)
		}
	})

	manifestDigest := registerBundleApp(t, ctx, appID, "Notes Bundle", "1.0.0", artifact.GetId(), artifact.GetDigest())
	if !strings.HasPrefix(manifestDigest, "sha256:") {
		t.Fatalf("manifest digest missing: %q", manifestDigest)
	}
	project := createIntegrationProject(t, ctx, projects, "Web Bundle Surface", fmt.Sprintf("surface-project-%d", stamp))
	installation := installApp(t, ctx, installations, fmt.Sprintf("surface-install-%d", stamp), project.GetId(), appID, "", project.GetRevision())
	if installation.GetVersion() != "1.0.0" || installation.GetManifestDigest() != manifestDigest {
		t.Fatalf("installation did not pin the bundle version: %#v", installation)
	}

	var session *surfacev1.SurfaceSession
	t.Run("CreateSurfaceResolvesInstalledInstance", func(t *testing.T) {
		key := fmt.Sprintf("surface-open-%d", stamp)
		response, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey:    key,
			AppInstanceId:     installation.GetId(),
			ProjectId:         project.GetId(),
			DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:          &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
			PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
		}))
		if err != nil {
			t.Fatalf("create surface: %v", err)
		}
		session = response.Msg.GetSession()
		if session.GetId() == "" || session.GetAppInstanceId() != installation.GetId() || session.GetProjectId() != project.GetId() {
			t.Fatalf("unexpected session identity: %#v", session)
		}
		if !isUUIDv7(session.GetId()) {
			t.Fatalf("session id is not UUIDv7: %q", session.GetId())
		}
		if session.GetRenderer() != surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE {
			t.Fatalf("unexpected renderer: %v", session.GetRenderer())
		}
		if session.GetUrl() != "/surfaces/"+session.GetId()+"/" {
			t.Fatalf("session url must be the same-origin relative path: %q", session.GetUrl())
		}
		// The bridge credential is now minted for every open session; the
		// app requested only artifact.read, so no bridge capability is
		// effective, and the unimplemented flags stay false.
		if len(session.GetBridgeToken()) != 43 {
			t.Fatalf("bridge token missing for an open session: %#v", session)
		}
		if len(session.GetBridgeCapabilities()) != 0 {
			t.Fatalf("unrequested capabilities leaked: %v", session.GetBridgeCapabilities())
		}
		if session.GetClipboard() || session.GetFilePicker() || session.GetResize() {
			t.Fatalf("unimplemented surface capabilities must stay false: %#v", session)
		}
		if session.GetCreatedAt() == nil || session.GetExpiresAt() == nil {
			t.Fatal("session times missing")
		}
		// Same key replay returns the first session; a different request aborts.
		replayed, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey:    key,
			AppInstanceId:     installation.GetId(),
			ProjectId:         project.GetId(),
			DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:          &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
			PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
		}))
		if err != nil || replayed.Msg.GetSession().GetId() != session.GetId() {
			t.Fatalf("surface replay failed: %v", err)
		}
		_, conflict := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey:    key,
			AppInstanceId:     installation.GetId(),
			ProjectId:         project.GetId(),
			DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:          &surfacev1.Viewport{Width: 640, Height: 480, PixelRatio: 1},
			PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
		}))
		if connect.CodeOf(conflict) != connect.CodeAborted {
			t.Fatalf("surface key conflict verdict: %v", conflict)
		}
		// A declared-but-unimplemented renderer is a stable InvalidArgument
		// that consumes nothing: the same key then opens with the implemented
		// renderer.
		_, unsupported := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey:    fmt.Sprintf("surface-renderer-%d", stamp),
			AppInstanceId:     installation.GetId(),
			ProjectId:         project.GetId(),
			DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:          &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
			PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_SERVICE,
		}))
		if connect.CodeOf(unsupported) != connect.CodeInvalidArgument {
			t.Fatalf("web-service renderer verdict: %v", unsupported)
		}
		declared, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey:    fmt.Sprintf("surface-renderer-%d", stamp),
			AppInstanceId:     installation.GetId(),
			ProjectId:         project.GetId(),
			DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:          &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
			PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
		}))
		if err != nil || declared.Msg.GetSession().GetId() == "" {
			t.Fatalf("rejected renderer poisoned the key: %v", err)
		}
		// A NaN pixel ratio is expressible in protobuf binary and must be an
		// invalid argument, not a digestable viewport.
		_, nan := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey: fmt.Sprintf("surface-nan-%d", stamp),
			AppInstanceId:  installation.GetId(),
			ProjectId:      project.GetId(),
			DeviceClass:    surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:       &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: math.NaN()},
		}))
		if connect.CodeOf(nan) != connect.CodeInvalidArgument {
			t.Fatalf("NaN viewport verdict: %v", nan)
		}
		// Unknown or foreign instances deny closed.
		_, unknown := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey: fmt.Sprintf("surface-unknown-%d", stamp),
			AppInstanceId:  newUUIDForTest(22),
			ProjectId:      project.GetId(),
			DeviceClass:    surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:       &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
		}))
		if connect.CodeOf(unknown) != connect.CodeNotFound {
			t.Fatalf("unknown instance verdict: %v", unknown)
		}
	})

	// The gateway injects one configured identity, so the cross-device part
	// of the idempotency contract is proven against runtime-host directly:
	// two trusted devices of the same owner share a key only up to the first
	// consumption; the second device gets a stable Aborted, the first keeps
	// replaying its exact session.
	t.Run("SurfaceIdempotencyBindsTrustedDevice", func(t *testing.T) {
		deviceA := directRuntimeClients(t, "0198d7ea-2110-7c42-b659-c5e4d73bc337", "0198d7ea-2110-7c42-b659-c5e4d73bc331")
		deviceB := directRuntimeClients(t, "0198d7ea-2110-7c42-b659-c5e4d73bc337", "0198d7ea-2110-7c42-b659-c5e4d73bc332")
		key := fmt.Sprintf("surface-device-%d", stamp)
		opened := func(client surfacev1connect.SurfaceServiceClient) *connect.Request[surfacev1.CreateSurfaceRequest] {
			request := connect.NewRequest(&surfacev1.CreateSurfaceRequest{
				IdempotencyKey: key,
				AppInstanceId:  installation.GetId(),
				ProjectId:      project.GetId(),
				DeviceClass:    surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
				Viewport:       &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
			})
			return request
		}
		first, err := deviceA.CreateSurface(ctx, opened(deviceA))
		if err != nil {
			t.Fatalf("device A create: %v", err)
		}
		// Same key, second trusted device: a stable abort decided by the
		// device-bound stored digest.
		_, conflict := deviceB.CreateSurface(ctx, opened(deviceB))
		if connect.CodeOf(conflict) != connect.CodeAborted {
			t.Fatalf("device B same-key verdict %v, want Aborted", conflict)
		}
		// The first device still replays its exact session.
		replayed, err := deviceA.CreateSurface(ctx, opened(deviceA))
		if err != nil || replayed.Msg.GetSession().GetId() != first.Msg.GetSession().GetId() {
			t.Fatalf("device A replay failed: %v", err)
		}
		// A different key on device B creates independently.
		independent := connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey: key + "-b",
			AppInstanceId:  installation.GetId(),
			ProjectId:      project.GetId(),
			DeviceClass:    surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:       &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
		})
		second, err := deviceB.CreateSurface(ctx, independent)
		if err != nil || second.Msg.GetSession().GetId() == first.Msg.GetSession().GetId() {
			t.Fatalf("independent key on device B failed: %v", err)
		}
		// Both sessions close cleanly afterwards so they cannot serve.
		for _, client := range []surfacev1connect.SurfaceServiceClient{deviceA, deviceB} {
			sessionID := first.Msg.GetSession().GetId()
			if client == deviceB {
				sessionID = second.Msg.GetSession().GetId()
			}
			if _, err := client.CloseSurface(ctx, connect.NewRequest(&surfacev1.CloseSurfaceRequest{SurfaceSessionId: sessionID})); err != nil {
				t.Fatalf("cleanup close failed: %v", err)
			}
		}
	})

	t.Run("AssetsAreServedWithSecurityHeaders", func(t *testing.T) {
		entry := httpGet(t, session.GetUrl())
		if entry.StatusCode != http.StatusOK {
			t.Fatalf("entrypoint status %d", entry.StatusCode)
		}
		body, _ := io.ReadAll(entry.Body)
		if !strings.Contains(string(body), "<title>Bundle</title>") {
			t.Fatalf("entrypoint bytes mismatch: %q", string(body[:min(64, len(body))]))
		}
		if got := entry.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Fatalf("server media type missing: %q", got)
		}
		csp := entry.Header.Get("Content-Security-Policy")
		for _, required := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'none'", "frame-ancestors 'self'", "sandbox allow-scripts"} {
			if !strings.Contains(csp, required) {
				t.Fatalf("CSP missing %q: %q", required, csp)
			}
		}
		for _, forbidden := range []string{"allow-same-origin", "allow-forms", "allow-popups", "allow-top-navigation", "allow-downloads", "allow-storage-access"} {
			if strings.Contains(csp, forbidden) {
				t.Fatalf("CSP grants dangerous sandbox token %q: %q", forbidden, csp)
			}
		}
		if entry.Header.Get("X-Content-Type-Options") != "nosniff" || entry.Header.Get("Cache-Control") != "no-store" || entry.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("hardening headers missing on the entrypoint")
		}
		script := httpGet(t, session.GetUrl()+"app.js")
		if script.StatusCode != http.StatusOK || script.Header.Get("Content-Type") != "text/javascript; charset=utf-8" {
			t.Fatalf("script asset status %d type %q", script.StatusCode, script.Header.Get("Content-Type"))
		}
		if script.Header.Get("ETag") == "" {
			t.Fatal("asset etag missing")
		}
	})

	t.Run("AssetPolicyFailsClosed", func(t *testing.T) {
		base := "/surfaces/" + session.GetId() + "/"
		for name, path := range map[string]string{
			"traversal":         base + "../index.html",
			"dot segment":       base + "./app.js",
			"dotdot segment":    base + "x/../app.js",
			"double slash":      base + "//app.js",
			"unknown file":      base + "missing.js",
			"unknown session":   "/surfaces/" + strings.Repeat("0", 36) + "/",
			"unknown type":      base + "nope.exe",
			"backslash":         base + `%5Capp.js`,
			"encoded traversal": base + "%2e%2e/app.js",
		} {
			if response := httpGet(t, path); response.StatusCode != http.StatusNotFound {
				t.Errorf("%s returned %d, want 404", name, response.StatusCode)
			}
		}
		if response := httpGet(t, base); response.StatusCode != http.StatusOK {
			t.Fatalf("entrypoint regressed to %d", response.StatusCode)
		}
		// Non-GET/HEAD methods are rejected by the asset route.
		request, _ := http.NewRequest(http.MethodPost, gatewayBaseURL()+base, nil)
		postResponse, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		postResponse.Body.Close()
		if postResponse.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("POST status %d", postResponse.StatusCode)
		}
	})

	t.Run("UninstallRevokesAssetsImmediately", func(t *testing.T) {
		if response := httpGet(t, session.GetUrl()); response.StatusCode != http.StatusOK {
			t.Fatalf("asset must serve before uninstall: %d", response.StatusCode)
		}
		refreshed, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: project.GetId()}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := installations.UninstallApp(ctx, connect.NewRequest(&appv1.UninstallAppRequest{
			IdempotencyKey: fmt.Sprintf("surface-uninstall-%d", stamp), ProjectId: project.GetId(),
			InstallationId: installation.GetId(), ExpectedProjectRevision: refreshed.Msg.GetProject().GetRevision(),
		})); err != nil {
			t.Fatalf("uninstall: %v", err)
		}
		if response := httpGet(t, session.GetUrl()); response.StatusCode != http.StatusNotFound {
			t.Fatalf("asset served after uninstall: %d", response.StatusCode)
		}
		if response := httpGet(t, session.GetUrl()+"app.js"); response.StatusCode != http.StatusNotFound {
			t.Fatalf("script served after uninstall: %d", response.StatusCode)
		}
	})

	t.Run("CloseIsIdempotentAndRevokesAssets", func(t *testing.T) {
		reinstalled := installApp(t, ctx, installations, fmt.Sprintf("surface-reinstall-%d", stamp), project.GetId(), appID, "", project.GetRevision()+2)
		response, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey: fmt.Sprintf("surface-reopen-%d", stamp),
			AppInstanceId:  reinstalled.GetId(),
			ProjectId:      project.GetId(),
			DeviceClass:    surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:       &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
		}))
		if err != nil {
			t.Fatalf("reopen after reinstall failed: %v", err)
		}
		reopened := response.Msg.GetSession()
		if httpGet(t, reopened.GetUrl()).StatusCode != http.StatusOK {
			t.Fatal("reopened session must serve")
		}
		if _, err := surfaces.CloseSurface(ctx, connect.NewRequest(&surfacev1.CloseSurfaceRequest{SurfaceSessionId: reopened.GetId()})); err != nil {
			t.Fatalf("first close failed: %v", err)
		}
		if _, err := surfaces.CloseSurface(ctx, connect.NewRequest(&surfacev1.CloseSurfaceRequest{SurfaceSessionId: reopened.GetId()})); err != nil {
			t.Fatalf("repeated close must succeed: %v", err)
		}
		if response := httpGet(t, reopened.GetUrl()); response.StatusCode != http.StatusNotFound {
			t.Fatalf("asset served after close: %d", response.StatusCode)
		}
		// Replay of the open key returns the closed snapshot, not a revival.
		replay, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey: fmt.Sprintf("surface-reopen-%d", stamp),
			AppInstanceId:  reinstalled.GetId(),
			ProjectId:      project.GetId(),
			DeviceClass:    surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:       &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
		}))
		if err != nil || replay.Msg.GetSession().GetId() != reopened.GetId() {
			t.Fatalf("replay after close must not revive: %v", err)
		}
		if httpGet(t, reopened.GetUrl()).StatusCode != http.StatusNotFound {
			t.Fatal("replayed closed session must not serve")
		}
		// Unknown and foreign sessions deny.
		if _, err := surfaces.CloseSurface(ctx, connect.NewRequest(&surfacev1.CloseSurfaceRequest{SurfaceSessionId: "0198d7ea-2110-7c42-b659-c5e4d73bc399"})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("unknown close verdict: %v", err)
		}
	})

	t.Run("LegacyAppsHaveNoSurface", func(t *testing.T) {
		legacyID := fmt.Sprintf("bundle-legacy-%d", stamp)
		registerApp(t, ctx, appRegistryClients(t), legacyID, "Legacy", "1.0.0", "user")
		legacyProject := createIntegrationProject(t, ctx, projects, "Web Bundle Legacy", fmt.Sprintf("surface-legacy-project-%d", stamp))
		legacyInstallation := installApp(t, ctx, installations, fmt.Sprintf("surface-legacy-install-%d", stamp), legacyProject.GetId(), legacyID, "", legacyProject.GetRevision())
		_, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey: fmt.Sprintf("surface-legacy-open-%d", stamp),
			AppInstanceId:  legacyInstallation.GetId(),
			ProjectId:      legacyProject.GetId(),
			DeviceClass:    surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:       &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 1},
		}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("legacy app surface verdict: %v", err)
		}
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isUUIDv7(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.Version() == 7 && parsed.Variant() == uuid.RFC4122
}
