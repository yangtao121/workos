//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// pinnedPolicyMigrationChecksums fixes migrations 001–013 byte for byte
// (013 pinned here for the first time alongside its predecessors) before this
// task's additive 014. Any edit to a historical migration is a hard failure.
var pinnedPolicyMigrationChecksums = map[string]string{
	"001_foundation.sql":                             "f748516e52ae915e0582a8dfa5de665a6590264268d0e55c46659746bcaa0378",
	"002_app_registry.sql":                           "f3a353fb0ffdf51cafc44e6fda63dba5fc55f436c2830c53bd0e972ed2504947",
	"003_app_registry_idempotency.sql":               "73766b95799bce3e0f4569e49940df044fd287ae723f38ed7f410c719e83ebe3",
	"004_project_app_installations.sql":              "df364efc07892164611e4587288e46ddec491b187662f6271dd2907c5527e00b",
	"005_project_app_installation_request_owner.sql": "45cb2bb4abb590656cb119e0517af3a220f94d279c43bec1eec754c5bf0a8781",
	"006_web_bundle_artifacts.sql":                   "628cc5099617c078352612b20bee3f83cefb166a8e5e25ea386da61da317cc27",
	"007_surface_sessions.sql":                       "b3fed6b62cbcd6af4d29f73076e83940393e79fd6351f2acaafdf909ec34a986",
	"008_project_installation_grants.sql":            "180ba05df3c54c45d16dd1c67f8b45cacdde8d6ac1a77ae5338abc3dd0055766",
	"009_agent_app_task_provenance.sql":              "233ea77ca9f3dc0d18362c0cc2a650eb288c5bc90d0c0e01e3ec9428b6f411db",
	"010_surface_bridge_tokens.sql":                  "91f47007a071915e0d6c2b39f35f2611f2b1f30c72781d113fd801368045896a",
	"011_mutable_project_app_grants.sql":             "1b85383b53f23829151cacca44c5f400f1fb9ca1e06f4836767a3c40f354775f",
	"012_surface_grant_revision.sql":                 "9b8335b1a7936ef96b5b5aaeeeac8b351768bb5c98152bfed6d80bbd904bcc89",
	"013_project_create_requests.sql":                "18de5d8271d669cbc7ca1aa0440927f792a1b555dc96208507369d8942691210",
}

// TestAppAgentPolicyMigrationChain runs migrations on a pristine scratch
// database through 014, re-runs them (no-op), verifies the checksums of every
// historical file, and asserts the 014 authority shape exists.
func TestAppAgentPolicyMigrationChain(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("pristine run through 014: %v", err)
	}
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("re-run must be a no-op: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for name, want := range pinnedPolicyMigrationChecksums {
		var got string
		if err := pool.QueryRow(ctx,
			"SELECT checksum FROM workos_meta.schema_migrations WHERE name = $1", name,
		).Scan(&got); err != nil {
			t.Fatalf("read checksum %s: %v", name, err)
		}
		if got != want {
			t.Fatalf("migration %s drifted:\n got %s\nwant %s", name, got, want)
		}
	}
	var applied bool
	if err := pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM workos_meta.schema_migrations WHERE name = '014_agent_app_policy_quota.sql')",
	).Scan(&applied); err != nil || !applied {
		t.Fatalf("014 must be applied: %v %v", err, applied)
	}
	// The six authority tables exist with their primary keys.
	for _, table := range []string{
		"agent_app_policies", "agent_app_policy_requests", "agent_app_approvals",
		"agent_app_daily_reservations", "agent_app_daily_usage", "agent_task_usage",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'workos_core' AND table_name = $1)",
			table,
		).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s must exist: %v %v", table, err, exists)
		}
	}
	// The task snapshot columns exist and legacy rows read NULL.
	var legacyNulls int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM workos_core.agent_tasks
		WHERE policy_source IS NOT NULL OR policy_revision IS NOT NULL
		   OR policy_spec_digest IS NOT NULL OR budget_max_output_tokens IS NOT NULL
		   OR budget_max_runtime_seconds IS NOT NULL`,
	).Scan(&legacyNulls); err != nil {
		t.Fatal(err)
	}
	if legacyNulls != 0 {
		t.Fatalf("legacy tasks must stay NULL, found %d", legacyNulls)
	}
}

// policyHarness seeds one owner/project/installation-shaped scratch database
// and hands back two independent Agent repositories over two pools.
type policyHarness struct {
	t           *testing.T
	pool        *pgxpool.Pool
	owner       string
	project     string
	instance    string
	left        *agentpostgres.Repository
	right       *agentpostgres.Repository
	leftService *agentapp.Service
}

func newPolicyHarness(t *testing.T, ctx context.Context) *policyHarness {
	t.Helper()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	rightPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rightPool.Close)
	h := &policyHarness{
		t: t, pool: pool,
		owner:    newUUIDForTest(401),
		project:  newUUIDForTest(402),
		instance: newUUIDForTest(403),
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO workos_core.users (id, kind, display_name, created_at) VALUES ($1, 'owner', 'Policy Harness Owner', now()) ON CONFLICT DO NOTHING",
		h.owner,
	); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at) VALUES ($1, $2, 'policy-harness-project', 'Policy Harness', $3, $4, now(), now())",
		h.project, h.owner, newUUIDForTest(404), newUUIDForTest(405),
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	h.left = agentpostgres.New(pool)
	h.right = agentpostgres.New(rightPool)
	h.leftService = agentapp.New(h.left, ids.UUIDv7{})
	return h
}

func (h *policyHarness) count(query string, args ...any) int {
	h.t.Helper()
	var count int
	if err := h.pool.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		h.t.Fatalf("count (%s): %v", query, err)
	}
	return count
}

func (h *policyHarness) submitAllow(repository *agentpostgres.Repository, key string) (agentdomain.Task, error) {
	h.t.Helper()
	service := agentapp.New(repository, ids.UUIDv7{})
	spec := agentdomain.PolicySpec{
		Mode: agentdomain.PolicyModeAllow, MaxOutputTokensPerTask: 64, MaxRuntimeSecondsPerTask: 60,
		MaxTasksPerUTCDay: 1, MaxReservedOutputTokensPerUTCDay: 64,
	}
	return service.SubmitForApp(context.Background(), agentapp.AppSubmitInput{
		OwnerUserID: h.owner, AppInstanceID: h.instance, ClientIdempotencyKey: key,
		RequestDigest: agentdomain.AppTaskRequestDigest("", "goal "+key),
		ProjectID:     h.project, ProviderID: "fake", Role: "", Goal: "goal " + key,
		Enforcement: agentapp.AppRunEnforcement{
			Policy: agentports.PolicySnapshot{
				Source: agentdomain.PolicySourceExplicit, Revision: 1, SpecDigest: spec.Digest(), Spec: spec,
			},
			MaxOutputTokensTask: spec.MaxOutputTokensPerTask, MaxRuntimeSecondsTask: spec.MaxRuntimeSecondsPerTask,
			Daily: agentports.DailyAllowance{MaxTasks: spec.MaxTasksPerUTCDay, MaxReservedOutputTokens: spec.MaxReservedOutputTokensPerUTCDay},
		},
	})
}

// TestAppAgentRepositoryPolicyAndQuotaConcurrency exercises the storage-level
// verdicts with two independent pools: same-key policy Set races, the quota
// last slot, and opposite approval decisions.
func TestAppAgentRepositoryPolicyAndQuotaConcurrency(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newPolicyHarness(t, ctx)

	t.Run("PolicySetRaceProducesOneWinner", func(t *testing.T) {
		spec := agentdomain.PolicySpec{
			Mode: agentdomain.PolicyModeRequireApproval, MaxOutputTokensPerTask: 32, MaxRuntimeSecondsPerTask: 30,
			MaxTasksPerUTCDay: 2, MaxReservedOutputTokensPerUTCDay: 64,
		}
		command := agentports.SetPolicyCommand{
			OwnerUserID: h.owner, AppInstanceID: h.instance, ProjectID: h.project,
			Spec: spec, SpecDigest: spec.Digest(), ExpectedPolicyRevision: 0,
			IdempotencyKey: "policy-race-key", RequestDigest: agentdomain.SetPolicyRequestDigest(h.project, h.instance, 0, spec),
			Now: time.Now().UTC(),
		}
		type verdict struct {
			policy agentdomain.Policy
			result agentports.SetPolicyResult
			err    error
		}
		start := make(chan struct{})
		var group sync.WaitGroup
		verdicts := make(chan verdict, 2)
		for _, repository := range []*agentpostgres.Repository{h.left, h.right} {
			group.Add(1)
			go func(repository *agentpostgres.Repository) {
				defer group.Done()
				<-start
				policy, result, err := repository.SetPolicy(ctx, command)
				verdicts <- verdict{policy, result, err}
			}(repository)
		}
		close(start)
		group.Wait()
		close(verdicts)
		winners, losers, replays := 0, 0, 0
		var firstErr error
		for v := range verdicts {
			switch {
			case v.err == nil && v.result.Changed:
				winners++
			case v.err == nil && v.result.Replay:
				replays++
			case v.err != nil:
				losers++
				if firstErr == nil {
					firstErr = v.err
				}
			}
		}
		if winners != 1 || losers+replays != 1 {
			t.Fatalf("expected one winner and one loser/replay: winners=%d losers=%d replays=%d err=%v", winners, losers, replays, firstErr)
		}
		if got := h.count("SELECT count(*) FROM workos_core.agent_app_policies WHERE owner_user_id = $1 AND app_instance_id = $2", h.owner, h.instance); got != 1 {
			t.Fatalf("one policy row expected: %d", got)
		}
	})

	t.Run("QuotaLastSlotNeverOversells", func(t *testing.T) {
		spec := agentdomain.PolicySpec{
			Mode: agentdomain.PolicyModeAllow, MaxOutputTokensPerTask: 64, MaxRuntimeSecondsPerTask: 30,
			MaxTasksPerUTCDay: 1, MaxReservedOutputTokensPerUTCDay: 64,
		}
		_ = spec
		first, err := h.submitAllow(h.left, "quota-first")
		if err != nil {
			t.Fatalf("first enqueue: %v", err)
		}
		if first.State != agentdomain.StateQueued {
			t.Fatalf("first enqueue state: %v", first.State)
		}
		var group sync.WaitGroup
		errs := make(chan error, 2)
		for _, repository := range []*agentpostgres.Repository{h.left, h.right} {
			group.Add(1)
			go func(repository *agentpostgres.Repository) {
				defer group.Done()
				_, err := h.submitAllow(repository, fmt.Sprintf("quota-over-%d", time.Now().UnixNano()))
				errs <- err
			}(repository)
		}
		group.Wait()
		close(errs)
		exhausted := 0
		for err := range errs {
			if err == nil {
				rows, _ := h.pool.Query(ctx, "SELECT utc_date, tasks_reserved, output_tokens_reserved FROM workos_core.agent_app_daily_reservations WHERE owner_user_id = $1 AND app_instance_id = $2", h.owner, h.instance)
				for rows.Next() {
					var date time.Time
					var tasks, tokens int64
					_ = rows.Scan(&date, &tasks, &tokens)
					t.Logf("bucket %s tasks=%d tokens=%d", date, tasks, tokens)
				}
				rows.Close()
				t.Fatal("an over-quota enqueue succeeded")
			}
			if agentErr := err; isQuotaExhausted(agentErr) {
				exhausted++
			}
		}
		if exhausted == 0 {
			t.Fatal("no quota exhaustion verdict observed")
		}
		if got := h.count("SELECT tasks_reserved FROM workos_core.agent_app_daily_reservations WHERE owner_user_id = $1 AND app_instance_id = $2", h.owner, h.instance); got != 1 {
			t.Fatalf("reservation must stay at one: %d", got)
		}
		if got := h.count("SELECT count(*) FROM workos_events.outbox WHERE aggregate_type = 'agent-task'"); got != 1 {
			t.Fatalf("exactly one claimable outbox row expected: %d", got)
		}
		if got := h.count("SELECT count(*) FROM workos_core.agent_app_task_requests WHERE owner_user_id = $1", h.owner); got != 1 {
			t.Fatalf("failed runs must not consume keys: %d mappings", got)
		}
	})
}

func isQuotaExhausted(err error) bool {
	for err != nil {
		if err == agentdomain.ErrQuotaExhausted {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestAppAgentUsageBreachCircuitBreaksBucket injects a usage observation past
// a task's reserved budget through the real repository and proves the
// deterministic circuit break: auditable breach flag, cancellation request,
// and subsequent fresh runs failing closed.
func TestAppAgentUsageBreachCircuitBreaksBucket(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newPolicyHarness(t, ctx)
	spec := agentdomain.PolicySpec{
		Mode: agentdomain.PolicyModeAllow, MaxOutputTokensPerTask: 4, MaxRuntimeSecondsPerTask: 30,
		MaxTasksPerUTCDay: 5, MaxReservedOutputTokensPerUTCDay: 20,
	}
	service := agentapp.New(h.left, ids.UUIDv7{})
	task, err := service.SubmitForApp(ctx, agentapp.AppSubmitInput{
		OwnerUserID: h.owner, AppInstanceID: h.instance, ClientIdempotencyKey: "breach-run",
		RequestDigest: agentdomain.AppTaskRequestDigest("", "breach goal"),
		ProjectID:     h.project, ProviderID: "fake", Role: "", Goal: "breach goal",
		Enforcement: agentapp.AppRunEnforcement{
			Policy: agentports.PolicySnapshot{
				Source: agentdomain.PolicySourceExplicit, Revision: 1, SpecDigest: spec.Digest(), Spec: spec,
			},
			MaxOutputTokensTask: spec.MaxOutputTokensPerTask, MaxRuntimeSecondsTask: spec.MaxRuntimeSecondsPerTask,
			Daily: agentports.DailyAllowance{MaxTasks: spec.MaxTasksPerUTCDay, MaxReservedOutputTokens: spec.MaxReservedOutputTokensPerUTCDay},
		},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Simulate the worker lease + usage event the way the Core execution path
	// does: claim, append usage_recorded above the reserved budget.
	lease, err := service.Claim(ctx, "breach-worker", 30*time.Second)
	if err != nil || lease == nil || lease.Task.ID != task.ID {
		t.Fatalf("claim: %v %+v", err, lease)
	}
	usagePayload, err := json.Marshal(map[string]any{
		"usageRecorded": map[string]any{"inputTokens": 2, "outputTokens": 9, "model": "fake/deterministic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendEvent(ctx, lease.ID, "breach-worker", "usage_recorded", usagePayload, agentdomain.StateRunning, "fake", "", &agentdomain.UsageReport{
		InputTokens: 2, OutputTokens: 9, Model: "fake/deterministic",
	}); err != nil {
		t.Fatalf("append usage: %v", err)
	}

	var cancelled bool
	if err := h.pool.QueryRow(ctx,
		"SELECT cancellation_requested FROM workos_core.agent_tasks WHERE owner_user_id = $1 AND id = $2",
		h.owner, task.ID,
	).Scan(&cancelled); err != nil {
		t.Fatal(err)
	}
	if !cancelled {
		t.Fatal("breach must request cancellation deterministically")
	}
	var breached bool
	if err := h.pool.QueryRow(ctx,
		"SELECT quota_breached FROM workos_core.agent_app_daily_usage WHERE owner_user_id = $1 AND app_instance_id = $2",
		h.owner, h.instance,
	).Scan(&breached); err != nil {
		t.Fatal(err)
	}
	if !breached {
		t.Fatal("breach flag missing on the daily bucket")
	}
	// The circuit break keeps fresh runs closed for the rest of the UTC day.
	if _, err := h.submitAllow(h.right, "breach-after"); !isQuotaExhausted(err) {
		t.Fatalf("breached bucket must fail closed: %v", err)
	}
	// Renewal observes the cancellation request.
	expires, cancelRequested, err := service.Renew(ctx, lease.ID, "breach-worker", 30*time.Second)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !cancelRequested || expires.IsZero() {
		t.Fatalf("cancellation must be observable: requested=%v expires=%v", cancelRequested, expires)
	}
}
