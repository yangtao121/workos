package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	desktopv1 "github.com/yangtao121/workos/gen/go/workos/desktop/v1"
	"github.com/yangtao121/workos/gen/go/workos/desktop/v1/desktopv1connect"
	"github.com/yangtao121/workos/internal/core/desktop/application"
	"github.com/yangtao121/workos/internal/core/desktop/testsupport"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
)

func rpc[T any](id identity.Identity, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	r.Header().Set(identity.UserHeader, id.UserID)
	r.Header().Set(identity.DeviceHeader, id.DeviceID)
	return r
}
func TestDesktopRPCAndResumableWatch(t *testing.T) {
	g := ids.UUIDv7{}
	id := identity.Identity{UserID: g.New(), DeviceID: g.New()}
	service := application.New(testsupport.NewStore(), &testsupport.References{}, g)
	h := NewHandler(service)
	h.poll = 5 * time.Millisecond
	h.heartbeat = 20 * time.Millisecond
	h.lifetime = 150 * time.Millisecond
	_, handler := desktopv1connect.NewDesktopServiceHandler(h, connect.WithReadMaxBytes(MaxRequestBytes))
	server := httptest.NewServer(identity.Middleware(handler))
	defer server.Close()
	client := desktopv1connect.NewDesktopServiceClient(server.Client(), server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.GetDesktop(ctx, connect.NewRequest(&desktopv1.GetDesktopRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal("missing auth accepted", err)
	}
	open := &desktopv1.ApplyDesktopOperationRequest{IdempotencyKey: "open", Operation: &desktopv1.ApplyDesktopOperationRequest_OpenWindow{OpenWindow: &desktopv1.DesktopWindowTarget{Kind: "home"}}}
	response, err := client.ApplyDesktopOperation(ctx, rpc(id, open))
	if err != nil {
		t.Fatal(err)
	}
	state := response.Msg.GetState()
	if state.GetRevision() != 1 || len(state.GetWindows()) != 1 {
		t.Fatal("wrong state", state)
	}
	watch, err := client.WatchDesktop(ctx, rpc(id, &desktopv1.WatchDesktopRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	if !watch.Receive() || watch.Msg().GetState().GetRevision() != 1 || watch.Msg().GetHeartbeat() {
		t.Fatal("missing durable catchup", watch.Err())
	}
	if !watch.Receive() || !watch.Msg().GetHeartbeat() {
		t.Fatal("missing handshake heartbeat")
	}
	close := &desktopv1.ApplyDesktopOperationRequest{IdempotencyKey: "close", Operation: &desktopv1.ApplyDesktopOperationRequest_CloseWindow{CloseWindow: &desktopv1.DesktopWindowSelection{WindowId: state.GetWindows()[0].GetId()}}}
	if _, err := client.ApplyDesktopOperation(ctx, rpc(id, close)); err != nil {
		t.Fatal(err)
	}
	found := false
	for watch.Receive() {
		message := watch.Msg()
		if !message.GetHeartbeat() && message.GetState().GetRevision() == 2 {
			found = true
			if len(message.GetState().GetWindows()) != 0 {
				t.Fatal("close not visible")
			}
			break
		}
	}
	if !found {
		t.Fatal("idle watch did not see remote operation", watch.Err())
	}
	for watch.Receive() {
	}
	if watch.Err() != nil {
		t.Fatal("bounded clean end", watch.Err())
	}
	resumed, err := client.WatchDesktop(ctx, rpc(id, &desktopv1.WatchDesktopRequest{AfterRevision: 1}))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if !resumed.Receive() || resumed.Msg().GetState().GetRevision() != 2 {
		t.Fatal("resume lost close", resumed.Err())
	}
	future, err := client.WatchDesktop(ctx, rpc(id, &desktopv1.WatchDesktopRequest{AfterRevision: 99}))
	if err != nil {
		t.Fatal(err)
	}
	defer future.Close()
	if !future.Receive() || !future.Msg().GetResetRequired() {
		t.Fatal("future cursor missing reset", future.Err())
	}
	other, err := client.GetDesktop(ctx, rpc(identity.Identity{UserID: g.New(), DeviceID: g.New()}, &desktopv1.GetDesktopRequest{}))
	if err != nil || other.Msg.GetState().GetRevision() != 0 {
		t.Fatal("cross owner leak", err)
	}
}
func TestDesktopWireBudgetAndInvalidOperations(t *testing.T) {
	g := ids.UUIDv7{}
	id := identity.Identity{UserID: g.New(), DeviceID: g.New()}
	_, handler := NewConnectHandler(application.New(testsupport.NewStore(), &testsupport.References{}, g))
	server := httptest.NewServer(identity.Middleware(handler))
	defer server.Close()
	client := desktopv1connect.NewDesktopServiceClient(server.Client(), server.URL)
	for _, r := range []*desktopv1.ApplyDesktopOperationRequest{{IdempotencyKey: "none"}, {IdempotencyKey: "unsafe", Operation: &desktopv1.ApplyDesktopOperationRequest_OpenWindow{OpenWindow: &desktopv1.DesktopWindowTarget{Kind: "https://credential"}}}, {IdempotencyKey: strings.Repeat("x", MaxRequestBytes*2), Operation: &desktopv1.ApplyDesktopOperationRequest_OpenWindow{OpenWindow: &desktopv1.DesktopWindowTarget{Kind: "home"}}}} {
		_, err := client.ApplyDesktopOperation(context.Background(), rpc(id, r))
		if connect.CodeOf(err) != connect.CodeInvalidArgument && connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Fatal("bad wire accepted", err)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL+desktopv1connect.DesktopServiceApplyDesktopOperationProcedure, strings.NewReader(strings.Repeat("x", MaxRequestBytes*2)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(identity.UserHeader, id.UserID)
	req.Header.Set(identity.DeviceHeader, id.DeviceID)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 400 {
		t.Fatal("oversized decode accepted")
	}
}

func TestWatchConnectionBudgetAndCleanup(t *testing.T) {
	g := ids.UUIDv7{}
	id := identity.Identity{UserID: g.New(), DeviceID: g.New()}
	h := NewHandler(application.New(testsupport.NewStore(), &testsupport.References{}, g))
	h.lifetime = 2 * time.Second
	_, handler := desktopv1connect.NewDesktopServiceHandler(h)
	server := httptest.NewServer(identity.Middleware(handler))
	defer server.Close()
	client := desktopv1connect.NewDesktopServiceClient(server.Client(), server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < WatchMaxPerOwner; i++ {
		stream, err := client.WatchDesktop(ctx, rpc(id, &desktopv1.WatchDesktopRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		if !stream.Receive() {
			t.Fatal("watch capacity unexpectedly unavailable", stream.Err())
		}
	}
	denied, err := client.WatchDesktop(ctx, rpc(id, &desktopv1.WatchDesktopRequest{}))
	if err == nil {
		defer denied.Close()
		for denied.Receive() {
		}
		err = denied.Err()
	}
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatal("unbounded owner streams", err)
	}
	cancel()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		h.mu.Lock()
		count := len(h.streams)
		h.mu.Unlock()
		if count == 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("disconnected watches leaked owner budget")
		case <-ticker.C:
		}
	}
}
