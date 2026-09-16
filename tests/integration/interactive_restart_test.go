//go:build integration

package integration_test

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/adapters/dockerpty"
	ptypostgres "github.com/yangtao121/workos/internal/runtime/ptyhost/adapters/postgres"
	ptyapp "github.com/yangtao121/workos/internal/runtime/ptyhost/application"
	ptyports "github.com/yangtao121/workos/internal/runtime/ptyhost/ports"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type restartWorkspace string

func (r restartWorkspace) WorkingDirectory(string, string) (string, bool) { return string(r), true }
func TestPtyContainerRestartKeepsIdentity(t *testing.T) {
	base := os.Getenv("WORKOS_WORKSPACE_TEST_ROOT")
	if base == "" {
		t.Skip("requires host-visible workspace and Docker socket")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root, err := os.MkdirTemp(base, "pty-restart-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	service, err := ptyapp.NewService(ptypostgres.New(pool), dockerpty.New("/var/run/docker.sock", "workos-workspace-runtime:dev"), ids.UUIDv7{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	service.WithWorkspace(restartWorkspace(root))
	gen := ids.UUIDv7{}
	owner, project, device := gen.New(), gen.New(), gen.New()
	created, err := service.Create(ctx, owner, project, "create", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background(), owner, created.SessionID)
	if _, err := service.Write(ctx, owner, device, created.SessionID, []byte("printf first > shared.txt\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if content, _ := os.ReadFile(filepath.Join(root, "shared.txt")); string(content) == "first" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "shared.txt")); string(content) != "first" {
		t.Fatal("terminal did not write the shared workspace")
	}
	// Foreign-owner Close previously reaped before checking ownership.
	if _, err := service.Close(ctx, gen.New(), created.SessionID); err == nil {
		t.Fatal("foreign close accepted")
	}
	if _, err := service.Stop(ctx, owner, created.SessionID, "first-stop", nil); err != nil {
		t.Fatal(err)
	}
	var fences atomic.Int32
	fence := func() error { fences.Add(1); return nil }
	restarted, err := service.Restart(ctx, owner, created.SessionID, "restart", fence)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.SessionID != created.SessionID || restarted.Generation != 2 {
		t.Fatalf("restart identity: %+v", restarted)
	}
	replay, err := service.Restart(ctx, owner, created.SessionID, "restart", fence)
	if err != nil || replay.Generation != 2 || fences.Load() != 1 {
		t.Fatalf("restart replay: %+v %v fences=%d", replay, err, fences.Load())
	}
	if _, err := service.Write(ctx, owner, device, created.SessionID, []byte("cat shared.txt; test ! -e /var/run/docker.sock && printf CONTAINER_OK\n")); err != nil {
		t.Fatal(err)
	}
	if old, err := service.Stop(ctx, owner, created.SessionID, "first-stop", func() error { t.Fatal("old stop fenced replacement"); return nil }); err != nil || old.State != "running" {
		t.Fatalf("old stop replay killed replacement: %+v %v", old, err)
	}
	cursor := int64(0)
	output := ""
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		next, data, _, err := service.Read(ctx, owner, created.SessionID, cursor, 65536)
		if err != nil {
			t.Fatal(err)
		}
		cursor = next
		output += string(data)
		if strings.Contains(output, "firstCONTAINER_OK") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(output, "firstCONTAINER_OK") {
		t.Fatalf("restarted terminal lost workspace: %q", output)
	}
}

type revocableWorkspace struct {
	root    string
	revoked atomic.Bool
}

func (w *revocableWorkspace) AuthorizeWorkspace(ctx context.Context, _, _ string) (ptyports.WorkspaceGrant, error) {
	if w.revoked.Load() {
		return ptyports.WorkspaceGrant{}, errors.New("revoked")
	}
	return ptyports.WorkspaceGrant{Directory: w.root, ReadOnly: true, Validate: func(context.Context) error {
		if w.revoked.Load() {
			return errors.New("revoked")
		}
		return nil
	}}, nil
}
func TestPtyContainerReadonlyAndRevocation(t *testing.T) {
	base := os.Getenv("WORKOS_WORKSPACE_TEST_ROOT")
	if base == "" {
		t.Skip("requires Docker and host-visible workspace")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, err := os.MkdirTemp(base, "pty-readonly-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.WriteFile(filepath.Join(root, "protected.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	service, err := ptyapp.NewService(ptypostgres.New(pool), dockerpty.New("/var/run/docker.sock", "workos-workspace-runtime:dev"), ids.UUIDv7{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	grant := &revocableWorkspace{root: root}
	service.WithWorkspaceAuthorization(grant)
	gen := ids.UUIDv7{}
	owner, project, device := gen.New(), gen.New(), gen.New()
	session, err := service.Create(ctx, owner, project, "readonly", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background(), owner, session.SessionID)
	if _, err := service.Write(ctx, owner, device, session.SessionID, []byte("printf changed > protected.txt; printf denied-write-finished\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	output := ""
	cursor := int64(0)
	for time.Now().Before(deadline) {
		next, data, _, err := service.Read(ctx, owner, session.SessionID, cursor, 65536)
		if err != nil {
			t.Fatal(err)
		}
		cursor = next
		output += string(data)
		if strings.Contains(output, "Read-only file system") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(output, "Read-only file system") {
		t.Fatalf("missing kernel write rejection: %q", output)
	}
	content, _ := os.ReadFile(filepath.Join(root, "protected.txt"))
	if string(content) != "original" {
		t.Fatal("readonly workspace changed")
	}
	grant.revoked.Store(true)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := service.Get(ctx, owner, session.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State.Terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	current, err := service.Get(ctx, owner, session.SessionID)
	if err != nil || current.State != "failed" {
		t.Fatalf("revocation left live process: %+v %v", current, err)
	}
	if _, err := service.Write(ctx, owner, device, session.SessionID, []byte("echo late\n")); err == nil {
		t.Fatal("revoked terminal accepted input")
	}
	if _, err := service.Create(ctx, owner, project, "denied", 80, 24); err == nil {
		t.Fatal("revoked grant started new terminal")
	}
}
