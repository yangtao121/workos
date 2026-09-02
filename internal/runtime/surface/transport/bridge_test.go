package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	bridgev1 "github.com/yangtao121/workos/gen/go/workos/bridge/v1"
	bridgev1connect "github.com/yangtao121/workos/gen/go/workos/bridge/v1/bridgev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

type fakeBridgeService struct {
	runErr     error
	streamErr  error
	submission ports.AppTaskSubmission
	lastToken  string
	ran        bool
	watched    bool
}

func (f *fakeBridgeService) RunAgentTask(_ context.Context, _, _, token, _, _, _ string) (ports.AppTaskSubmission, error) {
	f.ran = true
	f.lastToken = token
	if f.runErr != nil {
		return ports.AppTaskSubmission{}, f.runErr
	}
	return f.submission, nil
}

func (f *fakeBridgeService) StreamAgentEvents(context.Context, string, string, string, string, int64, func(*agentv1.AgentEvent) error) error {
	f.watched = true
	return f.streamErr
}

func newBridgeServer(t *testing.T, service *fakeBridgeService) bridgev1connect.AppBridgeServiceClient {
	t.Helper()
	path, handler := bridgev1connect.NewAppBridgeServiceHandler(NewBridge(service))
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return bridgev1connect.NewAppBridgeServiceClient(server.Client(), server.URL)
}

func runCall(client bridgev1connect.AppBridgeServiceClient, token, key, role, goal string) error {
	request := connect.NewRequest(&bridgev1.RunAgentTaskRequest{IdempotencyKey: key, Role: role, Goal: goal})
	// The trusted identity arrives on the wire via the gateway-injected
	// headers (trusted only behind the identity middleware on the server).
	request.Header().Set(identity.UserHeader, "owner-1")
	request.Header().Set(identity.DeviceHeader, "device-1")
	if token != "" {
		request.Header().Set(identity.BridgeTokenHeader, token)
	}
	_, err := client.RunAgentTask(context.Background(), request)
	return err
}

func identityContext(ctx context.Context) context.Context {
	return identity.WithContext(ctx, identity.Identity{UserID: "owner-1", DeviceID: "device-1"})
}

var bridgeValidToken = strings.Repeat("a", 43)

func TestBridgeRunSucceedsWithTokenMetadata(t *testing.T) {
	t.Parallel()
	service := &fakeBridgeService{submission: ports.AppTaskSubmission{TaskID: "task-1", State: "queued", LastEventSequence: 3}}
	client := newBridgeServer(t, service)
	err := runCall(client, bridgeValidToken, "key-1", "role", "goal")
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	// The service receives the token exactly as presented on the metadata.
	if !service.ran || service.lastToken != bridgeValidToken {
		t.Fatalf("token not forwarded as metadata: %q", service.lastToken)
	}
}

func TestBridgeRunFailsClosedWithoutIdentity(t *testing.T) {
	t.Parallel()
	service := &fakeBridgeService{}
	path, handler := bridgev1connect.NewAppBridgeServiceHandler(NewBridge(service))
	mux := http.NewServeMux()
	// No identity middleware: simulate a missing trusted identity context.
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(context.Background()))
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := bridgev1connect.NewAppBridgeServiceClient(server.Client(), server.URL)
	err := runCall(client, bridgeValidToken, "k", "", "g")
	assertBridgeCode(t, err, "unauthenticated")
	if service.ran {
		t.Fatal("service ran without identity")
	}
}

func TestBridgeRunErrorMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		runErr error
		token  string
		want   string
	}{
		{name: "invalid token", runErr: domain.ErrUnauthenticated, token: "bad", want: "unauthenticated"},
		{name: "expired credential", runErr: domain.ErrUnauthenticated, token: bridgeValidToken, want: "unauthenticated"},
		{name: "not granted", runErr: domain.ErrPermissionDenied, token: bridgeValidToken, want: "permission_denied"},
		{name: "core denied", runErr: ports.ErrAppAgentDenied, token: bridgeValidToken, want: "permission_denied"},
		{name: "task missing", runErr: domain.ErrNotFound, token: bridgeValidToken, want: "not_found"},
		{name: "core outage", runErr: ports.ErrAppAgentUnavailable, token: bridgeValidToken, want: "unavailable"},
		{name: "store outage", runErr: ports.ErrStoreUnavailable, token: bridgeValidToken, want: "unavailable"},
		{name: "unknown", runErr: errors.New("pgx commit failed"), token: bridgeValidToken, want: "internal"},
	}
	for _, testCase := range cases {
		service := &fakeBridgeService{runErr: testCase.runErr}
		client := newBridgeServer(t, service)
		err := runCall(client, testCase.token, "k", "", "g")
		assertBridgeCode(t, err, testCase.want)
	}
}

func TestBridgeRunRejectsMalformedInputBeforeService(t *testing.T) {
	t.Parallel()
	service := &fakeBridgeService{}
	client := newBridgeServer(t, service)
	if err := runCall(client, bridgeValidToken, "", "", "g"); !assertBridgeCode(t, err, "invalid_argument") || service.ran {
		t.Fatal("service ran on malformed key")
	}
	if err := runCall(client, bridgeValidToken, "k", "", ""); !assertBridgeCode(t, err, "invalid_argument") || service.ran {
		t.Fatal("service ran on empty goal")
	}
	if err := runCall(client, bridgeValidToken, "k", "", strings.Repeat("g", 16*1024+1)); !assertBridgeCode(t, err, "invalid_argument") || service.ran {
		t.Fatal("service ran on oversize goal")
	}
}

func TestBridgeWatchFailsClosedOnBadInput(t *testing.T) {
	t.Parallel()
	service := &fakeBridgeService{}
	client := newBridgeServer(t, service)

	// Streaming RPCs fail lazily: the verdict arrives on the first receive.
	receiveErr := func(request *connect.Request[bridgev1.WatchAgentTaskEventsRequest]) error {
		request.Header().Set(identity.UserHeader, "owner-1")
		request.Header().Set(identity.DeviceHeader, "device-1")
		stream, err := client.WatchAgentTaskEvents(context.Background(), request)
		if err != nil {
			return err
		}
		for stream.Receive() {
		}
		return stream.Err()
	}
	err := receiveErr(connect.NewRequest(&bridgev1.WatchAgentTaskEventsRequest{
		TaskId: validTaskID(), AfterSequence: -1,
	}))
	assertBridgeCode(t, err, "invalid_argument")
	if service.watched {
		t.Fatal("service ran on negative cursor")
	}

	err = receiveErr(connect.NewRequest(&bridgev1.WatchAgentTaskEventsRequest{
		TaskId: "short", AfterSequence: 0,
	}))
	assertBridgeCode(t, err, "invalid_argument")
	if service.watched {
		t.Fatal("service ran on short task id")
	}
}

func TestBridgeErrorsStaySanitized(t *testing.T) {
	t.Parallel()
	secret := "super-secret-token-value-aaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	service := &fakeBridgeService{runErr: errors.New("connection refused to 10.1.2.3:5432")}
	client := newBridgeServer(t, service)
	err := runCall(client, secret, "k", "", "g")
	assertBridgeCode(t, err, "internal")
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "5432") {
		t.Fatalf("error leaked internals: %v", err)
	}
}

// TestBridgeCoreDenialMapsToFixedPublicPermissionDenied pins the public end
// of the revocation error chain: the coreclient's sanitized private-denial
// sentinel (what Core returns on an installation grant epoch mismatch,
// ADR-0003 §7) surfaces as one fixed public PermissionDenied message with no
// revision value, grant, or Core detail.
func TestBridgeCoreDenialMapsToFixedPublicPermissionDenied(t *testing.T) {
	t.Parallel()
	service := &fakeBridgeService{runErr: fmt.Errorf("%w: %s", ports.ErrAppAgentDenied, "app capability is not granted")}
	client := newBridgeServer(t, service)
	err := runCall(client, bridgeValidToken, "k", "", "g")
	assertBridgeCode(t, err, "permission_denied")
	message := err.Error()
	if !strings.Contains(message, "bridge capability is not granted") {
		t.Fatalf("unexpected public denial message: %s", message)
	}
	for _, leak := range []string{"revision", "epoch", "grant revision", "core"} {
		if strings.Contains(message, leak) {
			t.Fatalf("public denial message leaks %q: %s", leak, message)
		}
	}
}

func assertBridgeCode(t *testing.T, err error, code string) bool {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error, got nil", code)
		return false
	}
	connectErr := &connect.Error{}
	if !errors.As(err, &connectErr) || connectErr.Code().String() != code {
		t.Fatalf("expected %s error, got %v", code, err)
		return false
	}
	return true
}

func validTaskID() string {
	return "0198d7ea-2110-7c42-b659-c5e4d73bc371"
}

func (s *fakeBridgeService) SearchKnowledge(context.Context, string, string, string, string, int32, string) (ports.KnowledgeSearchPage, error) {
	return ports.KnowledgeSearchPage{}, errors.New("not used in this test")
}
