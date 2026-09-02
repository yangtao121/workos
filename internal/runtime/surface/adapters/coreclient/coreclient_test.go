// Round-trip tests over the real Connect protocol: the adapters must map the
// private Core wire contract faithfully — the resolver's grant epoch, the
// session-derived epoch on private run/watch requests, the trusted identity
// headers — and collapse a Core denial into one sanitized sentinel so no Core
// authorization detail survives the boundary.
package coreclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// stubResolverService stands in for Core's private resolver.
type stubResolverService struct {
	response      *surfacev1.ResolveWebBundleResponse
	surfaceResult *surfacev1.ResolveSurfaceLaunchResponse
	request       *surfacev1.ResolveWebBundleRequest
	headers       http.Header
}

func (s *stubResolverService) ResolveSurfaceLaunch(_ context.Context, req *connect.Request[surfacev1.ResolveSurfaceLaunchRequest]) (*connect.Response[surfacev1.ResolveSurfaceLaunchResponse], error) {
	if s.surfaceResult != nil {
		return connect.NewResponse(s.surfaceResult), nil
	}
	return connect.NewResponse(&surfacev1.ResolveSurfaceLaunchResponse{
		Launch: &surfacev1.ResolveSurfaceLaunchResponse_WebBundle{WebBundle: &surfacev1.WebBundleLaunchDescriptor{
			AppId: s.response.GetLaunch().GetAppId(), Version: s.response.GetLaunch().GetVersion(),
			ManifestDigest: s.response.GetLaunch().GetManifestDigest(),
			ArtifactId:     s.response.GetLaunch().GetArtifactId(),
			ArtifactDigest: s.response.GetLaunch().GetArtifactDigest(),
			Entrypoint:     s.response.GetLaunch().GetEntrypoint(),
		}},
		GrantedPermissions: s.response.GetGrantedPermissions(),
		GrantRevision:      s.response.GetGrantRevision(),
	}), nil
}

func (s *stubResolverService) ResolveWebBundle(_ context.Context, req *connect.Request[surfacev1.ResolveWebBundleRequest]) (*connect.Response[surfacev1.ResolveWebBundleResponse], error) {
	s.request = req.Msg
	s.headers = req.Header()
	return connect.NewResponse(s.response), nil
}

func (s *stubResolverService) ReadWebBundleAsset(context.Context, *connect.Request[surfacev1.ReadWebBundleAssetRequest]) (*connect.Response[surfacev1.ReadWebBundleAssetResponse], error) {
	return connect.NewResponse(&surfacev1.ReadWebBundleAssetResponse{Content: []byte("x"), MediaType: "text/plain", Etag: "e"}), nil
}

// stubAppAgentService stands in for Core's private App Agent service.
type stubAppAgentService struct {
	response     *agentv1.RunAgentTaskResponse
	runErr       error
	watchErr     error
	runRequest   *agentv1.RunAgentTaskRequest
	watchRequest *agentv1.WatchAgentTaskEventsRequest
	runHeaders   http.Header
	watchHeaders http.Header
}

func (s *stubAppAgentService) RunAgentTask(_ context.Context, req *connect.Request[agentv1.RunAgentTaskRequest]) (*connect.Response[agentv1.RunAgentTaskResponse], error) {
	s.runRequest = req.Msg
	s.runHeaders = req.Header()
	if s.runErr != nil {
		return nil, s.runErr
	}
	return connect.NewResponse(s.response), nil
}

func (s *stubAppAgentService) WatchAgentTaskEvents(_ context.Context, req *connect.Request[agentv1.WatchAgentTaskEventsRequest], _ *connect.ServerStream[agentv1.WatchAgentTaskEventsResponse]) error {
	s.watchRequest = req.Msg
	s.watchHeaders = req.Header()
	return s.watchErr
}

func privateContext() context.Context {
	return identity.WithContext(context.Background(), identity.Identity{UserID: "owner-1", DeviceID: "device-1"})
}

func newResolverClient(t *testing.T, stub *stubResolverService) *Resolver {
	t.Helper()
	path, handler := surfacev1connect.NewSurfaceLaunchResolverServiceHandler(stub)
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	adapter, err := New(surfacev1connect.NewSurfaceLaunchResolverServiceClient(server.Client(), server.URL))
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func newAppAgentClient(t *testing.T, stub *stubAppAgentService) *AppAgent {
	t.Helper()
	path, handler := agentv1connect.NewAppAgentServiceHandler(stub)
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	adapter, err := NewAppAgent(agentv1connect.NewAppAgentServiceClient(server.Client(), server.URL))
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

// TestResolverMapsGrantRevision pins the epoch's first hop: Core's
// authoritative grant_revision crosses the port exactly as resolved — the
// application, not the adapter, decides whether to trust it.
func TestResolverMapsGrantRevision(t *testing.T) {
	t.Parallel()
	stub := &stubResolverService{response: &surfacev1.ResolveWebBundleResponse{
		Launch:             &surfacev1.WebBundleLaunchDescriptor{AppId: "notes-app", Version: "1.0.0", Entrypoint: "index.html"},
		GrantedPermissions: []string{"agent.task.run"},
		GrantRevision:      6,
	}}
	adapter := newResolverClient(t, stub)
	descriptor, err := adapter.ResolveWebBundle(privateContext(), ports.ResolveQuery{
		ProjectID: "0198d7ea-2110-7c42-b659-c5e4d73bc341", AppInstanceID: "0198d7ea-2110-7c42-b659-c5e4d73bc342",
	})
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.GrantRevision != 6 {
		t.Fatalf("grant revision = %d, want 6", descriptor.GrantRevision)
	}
	if stub.headers.Get(identity.UserHeader) != "owner-1" || stub.headers.Get(identity.DeviceHeader) != "device-1" {
		t.Fatalf("trusted identity not forwarded: %v", stub.headers)
	}
}

// TestAppAgentRunCarriesSessionEpoch pins the private run wire shape: the
// request's installation_grant_revision is exactly the session-derived value
// from the port query, alongside the trusted identity.
func TestAppAgentRunCarriesSessionEpoch(t *testing.T) {
	t.Parallel()
	stub := &stubAppAgentService{response: &agentv1.RunAgentTaskResponse{
		TaskId: "task-1", State: agentv1.AgentTaskState_AGENT_TASK_STATE_QUEUED, LastEventSequence: 0,
	}}
	adapter := newAppAgentClient(t, stub)
	submission, err := adapter.RunAgentTask(privateContext(), ports.AppAgentRunQuery{
		ProjectID: "0198d7ea-2110-7c42-b659-c5e4d73bc341", AppInstanceID: "0198d7ea-2110-7c42-b659-c5e4d73bc342",
		InstallationGrantRevision: 6, ClientKey: "client-key", Role: "role", Goal: "goal",
	})
	if err != nil || submission.TaskID != "task-1" {
		t.Fatalf("run failed: %+v %v", submission, err)
	}
	if stub.runRequest.GetInstallationGrantRevision() != 6 {
		t.Fatalf("run request epoch = %d, want 6", stub.runRequest.GetInstallationGrantRevision())
	}
	if stub.runHeaders.Get(identity.UserHeader) != "owner-1" || stub.runHeaders.Get(identity.DeviceHeader) != "device-1" {
		t.Fatalf("trusted identity not forwarded: %v", stub.runHeaders)
	}
}

// TestAppAgentWatchCarriesSessionEpoch pins the private watch wire shape: the
// stream request carries the same session-derived epoch Core re-authorizes
// on every polling round.
func TestAppAgentWatchCarriesSessionEpoch(t *testing.T) {
	t.Parallel()
	stub := &stubAppAgentService{}
	adapter := newAppAgentClient(t, stub)
	err := adapter.WatchAgentTaskEvents(privateContext(), ports.AppAgentWatchQuery{
		ProjectID: "0198d7ea-2110-7c42-b659-c5e4d73bc341", AppInstanceID: "0198d7ea-2110-7c42-b659-c5e4d73bc342",
		TaskID: "0198d7ea-2110-7c42-b659-c5e4d73bc371", AfterSequence: 3,
		InstallationGrantRevision: 6,
	}, func(*agentv1.AgentEvent) error { return nil })
	if err != nil {
		t.Fatalf("watch failed: %v", err)
	}
	if stub.watchRequest.GetInstallationGrantRevision() != 6 {
		t.Fatalf("watch request epoch = %d, want 6", stub.watchRequest.GetInstallationGrantRevision())
	}
	if stub.watchHeaders.Get(identity.UserHeader) != "owner-1" || stub.watchHeaders.Get(identity.DeviceHeader) != "device-1" {
		t.Fatalf("trusted identity not forwarded: %v", stub.watchHeaders)
	}
}

// TestAppAgentPrivateDenialIsSanitized pins the revocation error chain's first
// sanitization hop: a Core private PermissionDenied — whose internal message
// may name the epoch mismatch and current grants — collapses into the opaque
// ErrAppAgentDenied sentinel with a fixed short text. No revision value,
// grant, capability, or provider detail survives; the public bridge then maps
// the sentinel to one fixed PermissionDenied (transport matrix).
func TestAppAgentPrivateDenialIsSanitized(t *testing.T) {
	t.Parallel()
	stub := &stubAppAgentService{runErr: connect.NewError(connect.CodePermissionDenied,
		errors.New("installation grant revision mismatch: session=4 current=9 grants=[agent.task.run agent.event.watch] provider=deepseek"))}
	adapter := newAppAgentClient(t, stub)
	_, err := adapter.RunAgentTask(privateContext(), ports.AppAgentRunQuery{
		ProjectID: "0198d7ea-2110-7c42-b659-c5e4d73bc341", AppInstanceID: "0198d7ea-2110-7c42-b659-c5e4d73bc342",
		InstallationGrantRevision: 4, ClientKey: "client-key", Role: "role", Goal: "goal",
	})
	if !errors.Is(err, ports.ErrAppAgentDenied) {
		t.Fatalf("private denial verdict %v, want ErrAppAgentDenied", err)
	}
	message := err.Error()
	for _, leak := range []string{"mismatch", "session=4", "current=9", "4", "9", "agent.task.run", "deepseek", "grant revision"} {
		if strings.Contains(message, leak) {
			t.Fatalf("sanitized denial leaked %q: %s", leak, message)
		}
	}
}

func (s *stubAppAgentService) AuthorizeAppKnowledge(context.Context, *connect.Request[agentv1.AuthorizeAppKnowledgeRequest]) (*connect.Response[agentv1.AuthorizeAppKnowledgeResponse], error) {
	return nil, errors.New("not used in this test")
}
