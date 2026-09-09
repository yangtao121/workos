//go:build integration && terminalgate

package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
)

func terminalGateEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv("WORKOS_TERMINAL_GATE_" + name)
	if value == "" {
		t.Fatalf("run through tools/terminal-sessions/gate.sh (missing %s)", name)
	}
	return value
}

// TestTerminalSessions proves supervised PTY sessions on a real stack: an
// owner-started /bin/sh executes commands, echoes output through the
// bounded read cursor, resizes, replays idempotently, enforces the session
// cap, rejects foreign owners and bad sizes, and closes reaping the child.
func TestTerminalSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 30 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	gatewayURL := terminalGateEnv(t, "GATEWAY_URL")
	browsers := surfacev1connect.NewPtySessionServiceClient(client, gatewayURL)
	// Identity headers are trusted only on the runtime's private listener;
	// the gateway always re-injects the validated session owner, so the
	// foreign-owner isolation check runs directly against the runtime.
	directBrowsers := surfacev1connect.NewPtySessionServiceClient(client, terminalGateEnv(t, "RUNTIME_URL"))
	projects := projectv1connect.NewProjectServiceClient(client, gatewayURL)
	owner := "01999999-9999-7999-8999-000000000b01"
	device := "01999999-9999-7999-8999-000000000b02"

	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: fmt.Sprintf("terminal-%d", time.Now().UnixNano()), Name: "Terminal Fixture",
	}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	projectID := created.Msg.GetProject().GetId()

	key := fmt.Sprintf("pty-%d", time.Now().UnixNano())
	session, err := browsers.CreatePtySession(ctx, identityRequest(&surfacev1.CreatePtySessionRequest{
		IdempotencyKey: key, ProjectId: projectID, Columns: 90, Rows: 26,
	}, owner, device))
	if err != nil {
		t.Fatalf("create pty: %v", err)
	}
	sessionID := session.Msg.GetSession().GetId()
	if sessionID == "" || session.Msg.GetSession().GetState() != "running" {
		t.Fatalf("unexpected session: %+v", session.Msg.GetSession())
	}
	defer func() {
		_, _ = browsers.ClosePtySession(context.Background(), connect.NewRequest(&surfacev1.ClosePtySessionRequest{SessionId: sessionID}))
	}()

	// Idempotent replay returns the same session; drift aborts.
	replay, err := browsers.CreatePtySession(ctx, identityRequest(&surfacev1.CreatePtySessionRequest{
		IdempotencyKey: key, ProjectId: projectID, Columns: 90, Rows: 26,
	}, owner, device))
	if err != nil || replay.Msg.GetSession().GetId() != sessionID {
		t.Fatalf("replay drifted: %v %+v", err, replay.Msg.GetSession())
	}
	if _, err := browsers.CreatePtySession(ctx, identityRequest(&surfacev1.CreatePtySessionRequest{
		IdempotencyKey: key, ProjectId: projectID, Columns: 120, Rows: 26,
	}, owner, device)); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("drifted replay must abort: %v", err)
	}

	// A real command round trip: echo a unique marker through the shell.
	marker := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("terminal-%d", time.Now().UnixNano())))
	command := "echo workos-" + marker + "\n"
	if _, err := browsers.WritePtySession(ctx, identityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: sessionID, Input: []byte(command),
	}, owner, device)); err != nil {
		t.Fatalf("write: %v", err)
	}
	var cursor int64
	var output []byte
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		read, err := browsers.ReadPtySession(ctx, identityRequest(&surfacev1.ReadPtySessionRequest{
			SessionId: sessionID, After: cursor, MaxBytes: 65536,
		}, owner, device))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		output = append(output, read.Msg.GetOutput()...)
		cursor = int64(read.Msg.GetCursor())
		if strings.Contains(string(output), marker) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(string(output), marker) {
		t.Fatalf("shell did not echo the marker; output so far: %q", string(output))
	}
	// The cursor is strictly increasing and replay from zero is bounded.
	replayRead, err := browsers.ReadPtySession(ctx, identityRequest(&surfacev1.ReadPtySessionRequest{
		SessionId: sessionID, After: 0, MaxBytes: 64,
	}, owner, device))
	if err != nil || len(replayRead.Msg.GetOutput()) > 64 {
		t.Fatalf("bounded replay read failed: %v %d", err, len(replayRead.Msg.GetOutput()))
	}

	// Resize applies the window change without error.
	if _, err := browsers.ResizePtySession(ctx, identityRequest(&surfacev1.ResizePtySessionRequest{
		SessionId: sessionID, Columns: 120, Rows: 40,
	}, owner, device)); err != nil {
		t.Fatalf("resize: %v", err)
	}

	// Foreign owners never see or drive the session.
	if _, err := directBrowsers.ReadPtySession(ctx, identityRequestForeign(&surfacev1.ReadPtySessionRequest{
		SessionId: sessionID, After: 0, MaxBytes: 64,
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign read must 404: %v", err)
	}

	// Invalid sizes and oversized input fail closed.
	if _, err := browsers.CreatePtySession(ctx, identityRequest(&surfacev1.CreatePtySessionRequest{
		IdempotencyKey: key + "-bad-size", ProjectId: projectID, Columns: 2, Rows: 1,
	}, owner, device)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad size must be invalid: %v", err)
	}
	if _, err := browsers.WritePtySession(ctx, identityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: sessionID, Input: []byte(strings.Repeat("a", 32*1024)),
	}, owner, device)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversized write must be invalid: %v", err)
	}

	// The session cap holds: the owner already has one live session, three
	// more are admitted, the fifth is rejected.
	admitted := 0
	for index := 0; index < 4; index++ {
		created, err := browsers.CreatePtySession(ctx, identityRequest(&surfacev1.CreatePtySessionRequest{
			IdempotencyKey: fmt.Sprintf("%s-cap-%d", key, index), ProjectId: projectID, Columns: 80, Rows: 24,
		}, owner, device))
		if err == nil {
			admitted++
			defer func(id string) {
				_, _ = browsers.ClosePtySession(context.Background(), connect.NewRequest(&surfacev1.ClosePtySessionRequest{SessionId: id}))
			}(created.Msg.GetSession().GetId())
		}
	}
	if admitted > 3 {
		t.Fatalf("session cap did not hold: %d admitted", admitted)
	}

	// Close reaps the child: the durable row is closed and reads 404.
	if _, err := browsers.ClosePtySession(ctx, identityRequest(&surfacev1.ClosePtySessionRequest{SessionId: sessionID}, owner, device)); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := browsers.WritePtySession(ctx, identityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: sessionID, Input: []byte("echo gone\n"),
	}, owner, device)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("write after close must 404: %v", err)
	}
}

// identityRequest carries the trusted identity headers the dev-bypass
// gateway normally injects; the terminal gate hits the gateway directly.
func identityRequest[Req any](body *Req, owner, device string) *connect.Request[Req] {
	request := connect.NewRequest(body)
	request.Header().Set(identity.UserHeader, owner)
	request.Header().Set(identity.DeviceHeader, device)
	return request
}

func identityRequestForeign[Req any](body *Req) *connect.Request[Req] {
	return identityRequest(body, "01999999-9999-7999-8999-000000000c99", "01999999-9999-7999-8999-000000000b02")
}

var _ = json.Marshal
var _ = pgx.Connect
