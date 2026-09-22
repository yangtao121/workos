//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/core/desktop/adapters/postgres"
	"github.com/yangtao121/workos/internal/core/desktop/application"
	"github.com/yangtao121/workos/internal/core/desktop/domain"
	"github.com/yangtao121/workos/internal/core/desktop/testsupport"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectpostgres "github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("WORKOS_DESKTOP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set WORKOS_DESKTOP_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "desktop_" + strings.ReplaceAll(ids.UUIDv7{}.New(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Fatal("fixture DSN must use PostgreSQL URL form")
	}
	u.Path = "/" + name
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(ctx, cfg.ConnString()); err != nil {
		t.Fatal(err)
	}
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.Close()
		_, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	return p
}
func TestDesktopPostgresConcurrentReplayRestartAndRollback(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	g := ids.UUIDv7{}
	owner, project := g.New(), g.New()
	refs := &testsupport.References{}
	s := application.New(postgres.New(p), refs, g)
	op := domain.Operation{Kind: "open", Target: domain.Target{Kind: "files", ProjectID: project}}
	var wg sync.WaitGroup
	results := make(chan domain.State, 20)
	failures := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := s.Apply(ctx, owner, "same-key", op)
			if err != nil {
				failures <- err
			} else {
				results <- state
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	var window string
	for state := range results {
		if state.Revision != 1 || len(state.Windows) != 1 {
			t.Fatalf("race duplicated desktop %+v", state)
		}
		if window != "" && window != state.Windows[0].ID {
			t.Fatal("race reminted window")
		}
		window = state.Windows[0].ID
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := s.Apply(ctx, owner, fmt.Sprintf("resource-%d", n), domain.Operation{Kind: "open", Target: domain.Target{Kind: "artifact-viewer", ProjectID: project, ResourceKind: "artifact", ResourceID: g.New()}})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	state, err := s.Get(ctx, owner)
	if err != nil || state.Revision != 21 || len(state.Windows) != 21 {
		t.Fatalf("lost concurrent operation: %+v %v", state, err)
	}
	// A fresh connection pool and application have no in-memory state to rely on.
	fresh, err := pgxpool.New(ctx, p.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	restarted := application.New(postgres.New(fresh), refs, g)
	state, err = restarted.Apply(ctx, owner, "same-key", op)
	if err != nil || state.Revision != 21 {
		t.Fatal("restart replay did not return latest projection", err)
	}
	if _, err = restarted.Apply(ctx, owner, "same-key", domain.Operation{Kind: "open", Target: domain.Target{Kind: "home"}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("lost digest conflict", err)
	}
	state, err = restarted.Apply(ctx, owner, "close", domain.Operation{Kind: "close", WindowID: window})
	if err != nil {
		t.Fatal(err)
	}
	revision := state.Revision
	state, err = restarted.Apply(ctx, owner, "late-focus", domain.Operation{Kind: "focus", WindowID: window})
	if err != nil || state.Revision != revision || len(state.Windows) != 20 {
		t.Fatal("late focus resurrected window", err)
	}
	_, events, reset, err := restarted.Changes(ctx, owner, 0)
	if err != nil || reset || len(events) != int(revision) {
		t.Fatalf("durable event catchup count=%d rev=%d reset=%v err=%v", len(events), revision, reset, err)
	}
	refs.Denied = map[string]bool{project: true}
	if _, err = restarted.Apply(ctx, owner, "reusable", op); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("denial lost", err)
	}
	refs.Denied = nil
	if _, err = restarted.Apply(ctx, owner, "reusable", op); err != nil {
		t.Fatal("failed transaction consumed key", err)
	}
	var count int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM workos_core.desktop_operations WHERE owner_user_id=$1 AND idempotency_key='same-key'`, owner).Scan(&count); err != nil || count != 1 {
		t.Fatal("nonunique persisted idempotency", count, err)
	}
	other, err := restarted.Get(ctx, g.New())
	if err != nil || other.Revision != 0 || len(other.Windows) != 0 {
		t.Fatal("foreign owner snapshot leak", err)
	}
}
func TestDesktopPostgresRealProjectAuthorityAndPruning(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	g := ids.UUIDv7{}
	owner, foreign := g.New(), g.New()
	if _, err := p.Exec(ctx, `INSERT INTO workos_core.users(id,kind,display_name,created_at) VALUES($1,'owner','Shared desktop fixture',now()) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `SELECT id FROM workos_core.users WHERE kind='owner'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	projects := projectapp.New(projectpostgres.New(p), g)
	own, err := projects.Create(ctx, projectapp.CreateInput{OwnerUserID: owner, IdempotencyKey: g.New(), Name: "Desktop ownership fixture"})
	if err != nil {
		t.Fatal(err)
	}
	refs := &orchestration.DesktopReferences{Projects: projects}
	s := application.New(postgres.New(p), refs, g)
	for _, op := range []domain.Operation{{Kind: "switch", ProjectID: own.ID}, {Kind: "open", Target: domain.Target{Kind: "files", ProjectID: own.ID}}} {
		if _, err := s.Apply(ctx, foreign, g.New(), op); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("foreign owner project accepted", err)
		}
	}
	for _, op := range []domain.Operation{{Kind: "switch", ProjectID: g.New()}, {Kind: "open", Target: domain.Target{Kind: "files", ProjectID: g.New()}}} {
		if _, err := s.Apply(ctx, owner, g.New(), op); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("foreign project was accepted", err)
		}
	}
	state, err := s.Apply(ctx, owner, "initialize", domain.Operation{Kind: "initialize", ProjectID: own.ID, Windows: []domain.Target{{Kind: "home"}, {Kind: "files", ProjectID: own.ID}, {Kind: "files", ProjectID: g.New()}}})
	if err != nil || len(state.Windows) != 2 {
		t.Fatal("initial import not filtered", err)
	}
	if _, err := projects.Archive(ctx, owner, own.ID, own.Revision); err != nil {
		t.Fatal(err)
	}
	state, events, reset, err := s.Changes(ctx, owner, 0)
	if err != nil || !reset || len(events) != 0 || state.ActiveProjectID != "" || len(state.Windows) != 1 || state.Windows[0].Target.Kind != "home" {
		t.Fatalf("archived reference leaked: %+v %v %v", state, reset, err)
	}
}
