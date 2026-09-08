//go:build integration && browserpool

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type browserPoolStack struct {
	client   *http.Client
	gateway  string
	runtime  string
	browsers surfacev1connect.BrowserSessionServiceClient
	direct   surfacev1connect.BrowserSessionServiceClient
	projects projectv1connect.ProjectServiceClient
	owner    string
	device   string
	project  string
}

func browserPoolGateEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv("WORKOS_BROWSERPOOL_GATE_" + name)
	if value == "" {
		t.Fatalf("run through tools/browser-pool/gate.sh (missing %s)", name)
	}
	return value
}

func newBrowserPoolStack(t *testing.T) *browserPoolStack {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 60 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	gatewayURL := browserPoolGateEnv(t, "GATEWAY_URL")
	stack := &browserPoolStack{
		client:   client,
		gateway:  gatewayURL,
		runtime:  browserPoolGateEnv(t, "RUNTIME_URL"),
		browsers: surfacev1connect.NewBrowserSessionServiceClient(client, gatewayURL),
		direct:   surfacev1connect.NewBrowserSessionServiceClient(client, browserPoolGateEnv(t, "RUNTIME_URL")),
		projects: projectv1connect.NewProjectServiceClient(client, gatewayURL),
		owner:    "01999999-9999-7999-8999-000000000b01",
		device:   "01999999-9999-7999-8999-000000000b02",
	}
	created, err := stack.projects.CreateProject(context.Background(), connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: fmt.Sprintf("browser-pool-%d", time.Now().UnixNano()), Name: "Browser Pool Fixture",
	}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	stack.project = created.Msg.GetProject().GetId()
	stack.owner = created.Msg.GetProject().GetOwnerUserId()
	return stack
}

func (s *browserPoolStack) createRaw(t *testing.T, ctx context.Context, key, url string) (string, int) {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"idempotencyKey": key, "projectId": s.project, "initialUrl": url,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.gateway+"/workos.surface.v1.BrowserSessionService/CreateBrowserSession", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(identity.UserHeader, s.owner)
	request.Header.Set(identity.DeviceHeader, s.device)
	response, err := s.client.Do(request)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer response.Body.Close()
	var body struct {
		Session struct {
			Id    string `json:"id"`
			State string `json:"state"`
		} `json:"session"`
	}
	_ = json.NewDecoder(response.Body).Decode(&body)
	return body.Session.Id, response.StatusCode
}

func (s *browserPoolStack) close(t *testing.T, ctx context.Context, sessionID string) {
	t.Helper()
	if _, err := s.browsers.CloseBrowserSession(ctx, connect.NewRequest(&surfacev1.CloseBrowserSessionRequest{SessionId: sessionID})); err != nil {
		t.Fatalf("close session %s: %v", sessionID, err)
	}
}

// frameStats decodes a JPEG frame and returns (bytes, distinct-color count).
// A real rendered page has both; a blank screenshot collapses to few colors.
func frameStats(t *testing.T, jpegBytes []byte) (int, int) {
	t.Helper()
	decoded, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		t.Fatalf("frame is not valid JPEG: %v", err)
	}
	bounds := decoded.Bounds()
	colors := map[uint64]struct{}{}
	for y := bounds.Min.Y; y < bounds.Max.Y && len(colors) < 64; y += 8 {
		for x := bounds.Min.X; x < bounds.Max.X && len(colors) < 64; x += 8 {
			r, g, b, _ := decoded.At(x, y).RGBA()
			colors[uint64(r)<<32|uint64(g)<<16|uint64(b)] = struct{}{}
		}
	}
	return len(jpegBytes), len(colors)
}

// waitForFrame reads the watch stream until a frame with real content.
func (s *browserPoolStack) waitForFrame(t *testing.T, ctx context.Context, sessionID string, minColors int) (int, int) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		request := connect.NewRequest(&surfacev1.WatchBrowserSessionRequest{SessionId: sessionID})
		request.Header().Set(identity.UserHeader, s.owner)
		request.Header().Set(identity.DeviceHeader, s.device)
		stream, err := s.direct.WatchBrowserSession(ctx, request)
		if err != nil {
			t.Fatalf("watch browser session: %v", err)
		}
		for stream.Receive() {
			if frame := stream.Msg().GetFrame(); frame != nil && len(frame.GetJpeg()) > 0 {
				size, colors := frameStats(t, frame.GetJpeg())
				if colors >= minColors {
					stream.Close()
					return size, colors
				}
			}
		}
		if err := stream.Err(); err != nil {
			t.Fatalf("browser stream: %v", err)
		}
		stream.Close()
	}
	t.Fatal("browser stream produced no real frame")
	return 0, 0
}

// TestBrowserPoolRealChromium proves the pool against a real Chromium
// worker: navigation renders real pages, idempotency replays, drift aborts,
// close reaps, the session cap holds, and invalid schemes fail closed.
func TestBrowserPoolRealChromium(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	stack := newBrowserPoolStack(t)
	key := fmt.Sprintf("pool-%d", time.Now().UnixNano())

	sessionID, status := stack.createRaw(t, ctx, key, "http://127.0.0.1:8080/?tab=start")
	if status != http.StatusOK || sessionID == "" {
		t.Fatalf("session create failed: status=%d id=%q", status, sessionID)
	}
	defer stack.close(t, context.Background(), sessionID)

	size, _ := stack.waitForFrame(t, ctx, sessionID, 8)
	if size < 4<<10 {
		t.Fatalf("first frame too small: %d bytes", size)
	}

	replayID, _ := stack.createRaw(t, ctx, key, "http://127.0.0.1:8080/?tab=start")
	if replayID != sessionID {
		t.Fatalf("replay created a second session: %s != %s", replayID, sessionID)
	}
	_, driftStatus := stack.createRaw(t, ctx, key, "http://127.0.0.1:8080/?tab=review")
	if driftStatus != http.StatusConflict {
		t.Fatalf("drifted replay must abort, got %d", driftStatus)
	}

	navigated, err := stack.browsers.NavigateBrowserSession(ctx, connect.NewRequest(&surfacev1.NavigateBrowserSessionRequest{
		SessionId: sessionID, Url: "http://127.0.0.1:8080/?tab=review",
	}))
	if err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if !strings.Contains(navigated.Msg.GetSession().GetCurrentUrl(), "review") {
		t.Fatalf("navigate did not update the session: %+v", navigated.Msg.GetSession())
	}
	stack.waitForFrame(t, ctx, sessionID, 8)

	stack.close(t, ctx, sessionID)
	closed, err := stack.browsers.GetBrowserSession(ctx, connect.NewRequest(&surfacev1.GetBrowserSessionRequest{SessionId: sessionID}))
	if err != nil || closed.Msg.GetSession().GetState() != "closed" {
		t.Fatalf("closed session readback: %v %+v", err, closed.Msg.GetSession())
	}

	admitted := 0
	for index := 0; index < 5; index++ {
		id, status := stack.createRaw(t, ctx, fmt.Sprintf("%s-cap-%d", key, index), "http://127.0.0.1:8080/?tab=start")
		if status == http.StatusOK && id != "" {
			admitted++
			defer stack.close(t, context.Background(), id)
		}
	}
	if admitted != 4 {
		t.Fatalf("session cap did not hold: %d admitted (want exactly 4, the 5th rejected)", admitted)
	}

	if _, status := stack.createRaw(t, ctx, key+"-bad", "file:///etc/passwd"); status != http.StatusBadRequest {
		t.Fatalf("file:// scheme must be rejected, got %d", status)
	}
}

// TestBrowserPoolCrashSeed leaves one live session behind for gate.sh to
// crash its real Chromium worker between Seed and Restore.
func TestBrowserPoolCrashSeed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	stack := newBrowserPoolStack(t)
	key := fmt.Sprintf("crash-%d", time.Now().UnixNano())
	sessionID, status := stack.createRaw(t, ctx, key, "http://127.0.0.1:8080/?tab=docs")
	if status != http.StatusOK || sessionID == "" {
		t.Fatalf("crash seed create failed: status=%d", status)
	}
	stack.waitForFrame(t, ctx, sessionID, 8)
	state := map[string]string{"session": sessionID}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(browserPoolGateEnv(t, "DIR"), "crash-state.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestBrowserPoolCrashRestore verifies the crashed worker converges: the
// durable session either recovers with a recorded restart or terminates
// honestly at the restart bound — never silently wedged in running with a
// dead worker.
func TestBrowserPoolCrashRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	stack := newBrowserPoolStack(t)
	encoded, err := os.ReadFile(filepath.Join(browserPoolGateEnv(t, "DIR"), "crash-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]string
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}
	defer stack.close(t, context.Background(), state["session"])
	// A watch stream drives the capture loop: without an active consumer the
	// service has no reason to observe the crash and take over.
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	go func() {
		request := connect.NewRequest(&surfacev1.WatchBrowserSessionRequest{SessionId: state["session"]})
		request.Header().Set(identity.UserHeader, stack.owner)
		request.Header().Set(identity.DeviceHeader, stack.device)
		stream, err := stack.direct.WatchBrowserSession(watchCtx, request)
		if err != nil {
			return
		}
		for stream.Receive() {
		}
	}()
	deadline := time.Now().Add(75 * time.Second)
	var session *surfacev1.BrowserSession
	for time.Now().Before(deadline) {
		response, err := stack.browsers.GetBrowserSession(ctx, connect.NewRequest(&surfacev1.GetBrowserSessionRequest{SessionId: state["session"]}))
		if err != nil {
			t.Fatalf("read recovering session: %v", err)
		}
		session = response.Msg.GetSession()
		if (session.GetState() == "running" && session.GetRestartCount() >= 1) || session.GetState() == "failed" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if session == nil {
		t.Fatal("no session read")
	}
	if session.GetState() == "failed" && session.GetRestartCount() < 1 {
		t.Fatalf("failed without bounded restarts: %+v", session)
	}
	if session.GetState() == "running" && session.GetRestartCount() < 1 {
		t.Fatalf("running recovery without recorded restart: %+v", session)
	}
}
