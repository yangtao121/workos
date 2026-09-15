//go:build integration && workspacegate

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/yangtao121/workos/gen/go/workos/workload/v1"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
)

func workspaceGateEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv("WORKOS_WORKSPACE_GATE_" + name)
	if value == "" {
		t.Fatalf("run through tools/workspace-execution/gate.sh (missing %s)", name)
	}
	return value
}

// TestWorkspaceExecution proves one real project workspace (ADR-0030): the
// operator-registered git tree is discoverable, bindable through Core, and
// the owner's terminal shell runs inside that exact directory — pwd, git
// identity, and bidirectional file flow all resolve against the real disk
// tree, not copies.
func TestWorkspaceExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 30 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	gatewayURL := workspaceGateEnv(t, "URL")
	runtimeURL := workspaceGateEnv(t, "RUNTIME_URL")
	owner := workspaceGateEnv(t, "GITHUB_OWNER")
	project := workspaceGateEnv(t, "PROJECT_ID")
	repoTree := workspaceGateEnv(t, "REPO")
	expectedHead := workspaceGateEnv(t, "GIT_HEAD")

	workspaces := projectv1connect.NewProjectWorkspaceServiceClient(client, gatewayURL)
	pty := surfacev1connect.NewPtySessionServiceClient(client, gatewayURL)
	// The workspace host service is private: the identity headers are only
	// trusted on the runtime's own listener.
	host := workloadv1connect.NewWorkspaceHostServiceClient(client, runtimeURL)

	describe := connect.NewRequest(&workloadv1.DescribeWorkspaceSourcesRequest{})
	describe.Header().Set(identity.UserHeader, owner)
	describe.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
	describeResponse, err := host.DescribeWorkspaceSources(ctx, describe)
	if err != nil {
		t.Fatalf("describe sources: %v", err)
	}
	if len(describeResponse.Msg.GetSources()) != 1 {
		t.Fatalf("expected the registered source, got %d", len(describeResponse.Msg.GetSources()))
	}
	source := describeResponse.Msg.GetSources()[0]
	if source.GetKind() != "local_git" {
		t.Fatalf("registered kind: %q", source.GetKind())
	}

	// Unregistered source ids fail closed before any persistence.
	bindRejected := connect.NewRequest(&projectv1.BindWorkspaceRequest{
		ProjectId: project, WorkspaceSourceId: "ws_not_registered", DisplayName: "evil", IdempotencyKey: "workspace-gate-evil",
	})
	bindRejected.Header().Set(identity.UserHeader, owner)
	if _, err := workspaces.BindWorkspace(ctx, bindRejected); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unregistered source bind: %v", err)
	}

	bind := connect.NewRequest(&projectv1.BindWorkspaceRequest{
		ProjectId: project, WorkspaceSourceId: source.GetId(), DisplayName: "gate tree", IdempotencyKey: "workspace-gate-1",
	})
	bind.Header().Set(identity.UserHeader, owner)
	bound, err := workspaces.BindWorkspace(ctx, bind)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if bound.Msg.GetBinding().GetState() != projectv1.WorkspaceBindingState_WORKSPACE_BINDING_STATE_ACTIVE {
		t.Fatalf("binding state: %v", bound.Msg.GetBinding().GetState())
	}

	// The terminal session starts inside the bound workspace.
	create := connect.NewRequest(&surfacev1.CreatePtySessionRequest{ProjectId: project, Columns: 120, Rows: 30, IdempotencyKey: "workspace-gate-pty"})
	create.Header().Set(identity.UserHeader, owner)
	session, err := pty.CreatePtySession(ctx, create)
	if err != nil {
		t.Fatalf("create pty: %v", err)
	}
	sessionID := session.Msg.GetSession().GetId()
	defer func() {
		closeRequest := connect.NewRequest(&surfacev1.ClosePtySessionRequest{SessionId: sessionID})
		closeRequest.Header().Set(identity.UserHeader, owner)
		_, _ = pty.ClosePtySession(context.Background(), closeRequest)
	}()

	readUntil := func(t *testing.T, expect string, write string) string {
		t.Helper()
		if write != "" {
			writeRequest := connect.NewRequest(&surfacev1.WritePtySessionRequest{SessionId: sessionID, Input: []byte(write)})
			writeRequest.Header().Set(identity.UserHeader, owner)
			if _, err := pty.WritePtySession(ctx, writeRequest); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		var cursor int64
		var output strings.Builder
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			readRequest := connect.NewRequest(&surfacev1.ReadPtySessionRequest{SessionId: sessionID, After: cursor, MaxBytes: 32 * 1024})
			readRequest.Header().Set(identity.UserHeader, owner)
			readResponse, err := pty.ReadPtySession(ctx, readRequest)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			cursor = readResponse.Msg.GetCursor()
			output.Write(readResponse.Msg.GetOutput())
			if strings.Contains(output.String(), expect) {
				return output.String()
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("terminal never showed %q; output: %q", expect, output.String())
		return ""
	}

	// pwd resolves to the exact bound directory on the real disk.
	readUntil(t, "REALPWD:"+repoTree, "echo R\"EALPWD:$PWD\"\r")
	// git identity is the seeded commit, proving this is the registered
	// repository and not a copy. The runtime container ships no git binary
	// (recorded as a workspace toolchain gap), so the ref files are read
	// directly — same repository facts without the executable.
	readUntil(t, "REALHEAD:"+expectedHead, "echo R\"EALHEAD:$(cut -c6- .git/HEAD | xargs -I{} cat .git/{})\"\r")
	// The host-side marker file is the same file the shell sees.
	readUntil(t, "REALMARK:gate-marker", "echo R\"EALMARK:$(cat marker.txt)\"\r")

	// A command-side write reaches the real disk tree. The terminal echo
	// contains the command text itself, so success is proven by polling the
	// real file, not by matching terminal output.
	writeRequest := connect.NewRequest(&surfacev1.WritePtySessionRequest{SessionId: sessionID, Input: []byte("printf 'from-shell\\n' > shell-written.txt\r")})
	writeRequest.Header().Set(identity.UserHeader, owner)
	if _, err := pty.WritePtySession(ctx, writeRequest); err != nil {
		t.Fatalf("write: %v", err)
	}
	shellWritten := filepath.Join(repoTree, "shell-written.txt")
	var content []byte
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		content, err = os.ReadFile(shellWritten)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("shell write did not reach the disk tree: %v", err)
	}
	sum := sha256.Sum256(content)
	if string(content) != "from-shell\n" {
		t.Fatalf("shell-written content: %q", string(content))
	}

	// A host-side write is visible to the shell in the same directory.
	hostWritten := filepath.Join(repoTree, "host-written.txt")
	if err := os.WriteFile(hostWritten, []byte("from-host"), 0o644); err != nil {
		t.Fatalf("host write: %v", err)
	}
	readUntil(t, "REALHOST:from-host", "echo R\"EALHOST:$(cat host-written.txt)\"\r")

	_ = hex.EncodeToString(sum[:])

	// Archiving the binding blocks the next active-resolution read.
	archive := connect.NewRequest(&projectv1.ArchiveWorkspaceRequest{BindingId: bound.Msg.GetBinding().GetId(), ExpectedRevision: bound.Msg.GetBinding().GetRevision()})
	archive.Header().Set(identity.UserHeader, owner)
	if _, err := workspaces.ArchiveWorkspace(ctx, archive); err != nil {
		t.Fatalf("archive: %v", err)
	}
	list := connect.NewRequest(&projectv1.ListProjectWorkspacesRequest{ProjectId: project})
	list.Header().Set(identity.UserHeader, owner)
	listResponse, err := workspaces.ListProjectWorkspaces(ctx, list)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, binding := range listResponse.Msg.GetBindings() {
		if binding.GetState() == projectv1.WorkspaceBindingState_WORKSPACE_BINDING_STATE_ACTIVE {
			t.Fatal("archived binding still listed active")
		}
	}
}
