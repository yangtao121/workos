//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	notificationpostgres "github.com/yangtao121/workos/internal/core/notification/adapters/postgres"
	notificationapp "github.com/yangtao121/workos/internal/core/notification/application"
	notificationdomain "github.com/yangtao121/workos/internal/core/notification/domain"
	notificationports "github.com/yangtao121/workos/internal/core/notification/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// appendSearchFact projects one prepared system fact through the tx-scoped
// sink exactly as a Core producer would.
func appendSearchFact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repo *notificationpostgres.Repository, fact notificationdomain.SystemFact, occurredAt time.Time) {
	t.Helper()
	notification, err := notificationdomain.PrepareSystemFact(fact, occurredAt)
	if err != nil {
		t.Fatalf("prepare %s: %v", fact.Category, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, _, err := repo.AppendTx(ctx, tx, notification); err != nil {
		t.Fatalf("append %s: %v", fact.Category, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestNotificationSearch proves the bounded owner-scoped notification search
// (ADR-0018): case-insensitive title substring, deterministic ordering,
// signed pagination, closed-fail query grammar, and foreign-scope emptiness
// with no existence oracle.
func TestNotificationSearch(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := notificationpostgres.New(pool)
	generator := ids.UUIDv7{}
	service, err := notificationapp.New(repo, pool, generator)
	if err != nil {
		t.Fatal(err)
	}

	// Single-owner deployment: exactly one owner row may exist (foundation
	// constraint), which is also the isolation guarantee the foreign probe
	// below relies on.
	owner := "01999999-9999-7999-8999-000000000f01"
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1::uuid, 'owner', 'Notification Search Owner', now())
		ON CONFLICT (id) DO NOTHING`, owner); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	now := time.Now().UTC()
	appendSearchFact(t, ctx, pool, repo, notificationdomain.SystemFact{
		Kind: notificationdomain.KindAgentTaskTerminal, OwnerUserID: owner,
		TargetID: "01999999-9999-7999-8999-000000000fa1", Category: "completed",
		SourceID: "01999999-9999-7999-8999-000000000fb1",
	}, now.Add(-2*time.Hour))
	appendSearchFact(t, ctx, pool, repo, notificationdomain.SystemFact{
		Kind: notificationdomain.KindAgentTaskTerminal, OwnerUserID: owner,
		TargetID: "01999999-9999-7999-8999-000000000fa2", Category: "failed",
		SourceID: "01999999-9999-7999-8999-000000000fb2",
	}, now.Add(-1*time.Hour))

	// Case-insensitive substring; deterministic created DESC ordering.
	for _, probe := range []struct {
		query    string
		contains string
		wantHits int
	}{
		{query: "completed", contains: "completed", wantHits: 1},
		{query: "TASK FAILED", contains: "failed", wantHits: 1},
		{query: "task", contains: "task", wantHits: 2},
		{query: "zzz-no-match", contains: "", wantHits: 0},
	} {
		page, next, err := service.Search(ctx, owner, probe.query, notificationports.Filter{}, 20, "")
		if err != nil {
			t.Fatalf("search %q: %v", probe.query, err)
		}
		if len(page.Notifications) != probe.wantHits {
			t.Fatalf("search %q hits = %d, want %d", probe.query, len(page.Notifications), probe.wantHits)
		}
		if probe.wantHits == 1 && !strings.Contains(strings.ToLower(page.Notifications[0].Title), probe.contains) {
			t.Fatalf("search %q hit title %q", probe.query, page.Notifications[0].Title)
		}
		if next != "" {
			t.Fatalf("search %q produced an unexpected continuation", probe.query)
		}
	}

	// The query grammar fails closed before any read: empty, oversized, and
	// control-character queries are the same invalid verdict.
	for _, bad := range []string{"", "   ", strings.Repeat("x", 129), "bad\x00query", "bad\nquery"} {
		if _, _, err := service.Search(ctx, owner, bad, notificationports.Filter{}, 20, ""); err == nil {
			t.Fatalf("query %q must fail closed", bad)
		}
	}

	// A foreign owner sees an empty page for the same query — no existence
	// oracle beyond emptiness.
	foreign := "01999999-9999-7999-8999-000000000f02"
	page, _, err := service.Search(ctx, foreign, "completed", notificationports.Filter{}, 20, "")
	if err != nil {
		t.Fatalf("foreign search: %v", err)
	}
	if len(page.Notifications) != 0 {
		t.Fatalf("foreign owner saw %d hits", len(page.Notifications))
	}

	// Pagination walks deterministically: pageSize=1 over "task" yields both
	// facts in the same order as the full page.
	full, _, err := service.Search(ctx, owner, "task", notificationports.Filter{}, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	var walked []string
	token := ""
	for {
		page, next, err := service.Search(ctx, owner, "task", notificationports.Filter{}, 1, token)
		if err != nil {
			t.Fatalf("page walk: %v", err)
		}
		if len(page.Notifications) != 1 {
			t.Fatalf("walk page size = %d", len(page.Notifications))
		}
		walked = append(walked, page.Notifications[0].ID)
		if next == "" {
			break
		}
		token = next
		if len(walked) > 5 {
			t.Fatal("search token chain never terminated")
		}
	}
	if len(walked) != len(full.Notifications) {
		t.Fatalf("walked %d, full page %d", len(walked), len(full.Notifications))
	}
	for i := range walked {
		if walked[i] != full.Notifications[i].ID {
			t.Fatalf("walk diverged at %d", i)
		}
	}
}
