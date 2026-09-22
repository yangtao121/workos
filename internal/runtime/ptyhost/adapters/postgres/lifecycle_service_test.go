package postgres

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/adapters/shellexec"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/application"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/domain"
)

func TestManualStopRealShellAndPersistence(t *testing.T) {
	repository := lifecycleRepository(t)
	engine, err := shellexec.New("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	generator := ids.UUIDv7{}
	owner, project, key := generator.New(), generator.New(), generator.New()
	service, err := application.NewService(repository, engine, generator, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	session, err := service.Create(ctx, owner, project, key, 80, 24, domain.LifecycleManualStop)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(ctx, owner, session.SessionID)
	if session.LifecycleMode != domain.LifecycleManualStop || !session.ExpiresAt.IsZero() {
		t.Fatal("manual policy missing")
	}
	replay, err := service.Create(ctx, owner, project, key, 80, 24, domain.LifecycleManualStop)
	if err != nil || replay.SessionID != session.SessionID {
		t.Fatalf("replay started another shell: %v", err)
	}
	if _, err := service.Create(ctx, owner, project, key, 80, 24); !errors.Is(err, domain.ErrIdempotencyDrift) {
		t.Fatalf("mode drift accepted: %v", err)
	}
	if _, err := service.Detach(ctx, owner, session.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ExpireIdle(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Write(ctx, owner, "", session.SessionID, []byte("printf 'manual-still-running\\n'\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var output []byte
	for time.Now().Before(deadline) {
		_, output, _, err = service.Read(ctx, owner, session.SessionID, 0, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if len(output) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(output) == 0 {
		t.Fatal("real terminal produced no output")
	}
	restartKey := generator.New()
	next, err := service.Restart(ctx, owner, session.SessionID, restartKey, nil, domain.LifecycleManualStop)
	if err != nil || next.Generation != 2 {
		t.Fatalf("restart: %#v %v", next, err)
	}
	if _, err := service.Restart(ctx, owner, session.SessionID, restartKey, nil, domain.LifecycleBounded); !errors.Is(err, domain.ErrIdempotencyDrift) {
		t.Fatalf("restart drift: %v", err)
	}
	if _, err := service.Write(ctx, owner, "", session.SessionID, []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := service.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		next, err = service.Get(ctx, owner, session.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if next.State.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !next.State.Terminal() {
		t.Fatal("natural exit stayed running without TTL")
	}
}
