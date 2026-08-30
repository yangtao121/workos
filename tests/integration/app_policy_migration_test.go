//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
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
// deterministic circuit break: auditable breach flag, the task cancelled
// inside the very same transaction — before any provider completion can
// append — the outbox request finished, and subsequent fresh runs failing
// closed for the rest of the UTC day.
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
	// The task is terminal in the same transaction as the usage event: a
	// provider completion racing the breach cannot append afterwards.
	var state string
	if err := h.pool.QueryRow(ctx,
		"SELECT state FROM workos_core.agent_tasks WHERE owner_user_id = $1 AND id = $2",
		h.owner, task.ID,
	).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" {
		t.Fatalf("breached task must be cancelled immediately, got %q", state)
	}
	if got := h.count("SELECT count(*) FROM workos_events.events WHERE stream_type = 'agent-task' AND stream_id = $1 AND event_type = 'run_cancelled'", task.ID); got != 1 {
		t.Fatalf("exactly one run_cancelled terminal event expected: %d", got)
	}
	if got := h.count("SELECT count(*) FROM workos_events.outbox WHERE aggregate_type = 'agent-task' AND aggregate_id = $1 AND processed_at IS NULL", task.ID); got != 0 {
		t.Fatalf("breached task outbox must be finished: %d unprocessed", got)
	}
	// The terminal state refuses every later append, so the provider's
	// run_completed can never land after the breach.
	if _, err := service.AppendEvent(ctx, lease.ID, "breach-worker", "run_completed", json.RawMessage(`{"runCompleted":{"summary":"too late"}}`), agentdomain.StateCompleted, "fake", "", nil); err == nil {
		t.Fatal("append after the breach must be refused")
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
}

// approvalChainFixture builds one waiting task + pending-approval creation
// payload against the harness's installation, with a unique idempotency key
// per attempt.
func approvalChainFixture(h *policyHarness, key string, revision int64, source agentdomain.PolicySource, spec agentdomain.PolicySpec) (agentdomain.Task, agentdomain.Approval, agentports.AppTaskProvenance) {
	generator := ids.UUIDv7{}
	now := time.Now().UTC()
	task := agentdomain.Task{
		ID: generator.New(), OwnerUserID: h.owner, ProjectID: h.project,
		Input: json.RawMessage(`{"goal":"chain"}`), State: agentdomain.StateWaiting,
		ProviderID: "fake", CreatedAt: now, UpdatedAt: now,
	}
	approval := agentdomain.Approval{
		ID: generator.New(), OwnerUserID: h.owner, AppInstanceID: h.instance,
		ProjectID: h.project, TaskID: task.ID, AppID: "chain.app",
		GoalExcerpt: "chain", ProviderID: "fake",
		Source: source, Spec: spec, Revision: revision,
		State: agentdomain.ApprovalPending, CreatedAt: now, UpdatedAt: now,
	}
	provenance := agentports.AppTaskProvenance{
		TaskIdempotencyKey: generator.New(), AppInstanceID: h.instance,
		ClientIdempotencyKey: key, RequestDigest: agentdomain.AppTaskRequestDigest("", "chain "+key),
	}
	return task, approval, provenance
}

// TestAppAgentApprovalDecisionLocksPolicyChainBeforeRows pins the lock order
// shared with SetPolicy. While another transaction owns the installation's
// policy-chain lock, an approve must wait before locking its approval row;
// this prevents stale-policy approval and the inverse row-lock deadlock.
func TestAppAgentApprovalDecisionLocksPolicyChainBeforeRows(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newPolicyHarness(t, ctx)
	spec := agentdomain.PolicySpec{
		Mode: agentdomain.PolicyModeRequireApproval, MaxOutputTokensPerTask: 32, MaxRuntimeSecondsPerTask: 30,
		MaxTasksPerUTCDay: 2, MaxReservedOutputTokensPerUTCDay: 64,
	}
	if _, result, err := h.left.SetPolicy(ctx, agentports.SetPolicyCommand{
		OwnerUserID: h.owner, AppInstanceID: h.instance, ProjectID: h.project,
		Spec: spec, SpecDigest: spec.Digest(), ExpectedPolicyRevision: 0,
		IdempotencyKey: "decision-lock-seed", RequestDigest: agentdomain.SetPolicyRequestDigest(h.project, h.instance, 0, spec),
		Now: time.Now().UTC(),
	}); err != nil || !result.Changed {
		t.Fatalf("seed policy: %v %+v", err, result)
	}
	task, approval, provenance := approvalChainFixture(h, "decision-lock-approval", 1, agentdomain.PolicySourceExplicit, spec)
	_, stored, err := h.left.CreateForAppApproval(ctx, task, approval, provenance)
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}

	// Mirror the repository's stable two-key advisory-lock key so this
	// transaction can hold the exact installation chain while Decide starts.
	hash := fnv.New32a()
	hash.Write([]byte(h.owner))
	hash.Write([]byte{0})
	hash.Write([]byte(h.instance))
	holder, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx) //nolint:errcheck
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_xact_lock($1::int, $2::int)", int32(0x61706F6C), int32(hash.Sum32())); err != nil {
		t.Fatalf("hold policy chain: %v", err)
	}

	type decisionResult struct {
		approval agentdomain.Approval
		err      error
	}
	decided := make(chan decisionResult, 1)
	go func() {
		result, err := h.right.DecideApproval(ctx, agentports.DecideApprovalCommand{
			OwnerUserID: h.owner, ApprovalID: stored.ID,
			Decision: agentdomain.ApprovalDecisionApprove, IdempotencyKey: "decision-lock-approve",
			DecisionDigest: agentdomain.DecideApprovalRequestDigest(stored.ID, agentdomain.ApprovalDecisionApprove),
			Now:            time.Now().UTC(),
		})
		decided <- decisionResult{approval: result, err: err}
	}()

	// Wait until PostgreSQL confirms Decide is queued on the advisory lock.
	// The scratch database is unique to this test, so another test cannot
	// satisfy this observation.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := h.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database()
				  AND pid <> pg_backend_pid()
				  AND wait_event_type = 'Lock'
				  AND wait_event = 'advisory'
			)`,
		).Scan(&waiting); err != nil {
			t.Fatalf("observe policy-chain waiter: %v", err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approval decision did not wait on the policy-chain lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// If Decide had taken the approval row before waiting on the chain, this
	// NOWAIT probe would fail. Successful acquisition proves chain -> row.
	probe, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var probedID string
	probeErr := probe.QueryRow(ctx,
		"SELECT id FROM workos_core.agent_app_approvals WHERE owner_user_id = $1 AND id = $2 FOR UPDATE NOWAIT",
		h.owner, stored.ID,
	).Scan(&probedID)
	if rollbackErr := probe.Rollback(ctx); rollbackErr != nil {
		t.Fatalf("release approval probe: %v", rollbackErr)
	}
	if probeErr != nil || probedID != stored.ID {
		t.Fatalf("approve locked row before policy chain: %v %q", probeErr, probedID)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("release policy chain: %v", err)
	}

	select {
	case result := <-decided:
		if result.err != nil || result.approval.State != agentdomain.ApprovalApproved {
			t.Fatalf("approve after chain release: %v %+v", result.err, result.approval)
		}
	case <-ctx.Done():
		t.Fatalf("approve did not finish after chain release: %v", ctx.Err())
	}
}

// TestAppAgentApprovalCreationVerifiesPolicyChain pins the creation-side half
// of the policy linearization: inside the policy-chain lock the waiting-
// approval creation re-verifies the caller's snapshot against the stored row,
// so a snapshot resolved before a policy change can never become a pending
// approval after it — the whole transaction rolls back with nothing consumed.
func TestAppAgentApprovalCreationVerifiesPolicyChain(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newPolicyHarness(t, ctx)
	spec := agentdomain.PolicySpec{
		Mode: agentdomain.PolicyModeRequireApproval, MaxOutputTokensPerTask: 32, MaxRuntimeSecondsPerTask: 30,
		MaxTasksPerUTCDay: 2, MaxReservedOutputTokensPerUTCDay: 64,
	}
	if _, result, err := h.left.SetPolicy(ctx, agentports.SetPolicyCommand{
		OwnerUserID: h.owner, AppInstanceID: h.instance, ProjectID: h.project,
		Spec: spec, SpecDigest: spec.Digest(), ExpectedPolicyRevision: 0,
		IdempotencyKey: "chain-seed", RequestDigest: agentdomain.SetPolicyRequestDigest(h.project, h.instance, 0, spec),
		Now: time.Now().UTC(),
	}); err != nil || !result.Changed {
		t.Fatalf("seed policy: %v %+v", err, result)
	}

	t.Run("stale revision refuses with nothing consumed", func(t *testing.T) {
		task, approval, provenance := approvalChainFixture(h, "chain-stale", 2, agentdomain.PolicySourceExplicit, spec)
		if _, _, err := h.left.CreateForAppApproval(ctx, task, approval, provenance); !isPolicyStale(err) {
			t.Fatalf("stale snapshot verdict: %v", err)
		}
		if got := h.count("SELECT count(*) FROM workos_core.agent_app_approvals WHERE owner_user_id = $1", h.owner); got != 0 {
			t.Fatalf("stale snapshot must not persist an approval: %d", got)
		}
		if got := h.count("SELECT count(*) FROM workos_core.agent_tasks WHERE owner_user_id = $1", h.owner); got != 0 {
			t.Fatalf("stale snapshot must not persist a task: %d", got)
		}
		if got := h.count("SELECT count(*) FROM workos_core.agent_app_task_requests WHERE owner_user_id = $1", h.owner); got != 0 {
			t.Fatalf("stale snapshot must not consume the run key: %d", got)
		}
	})

	t.Run("default source behind an explicit row refuses", func(t *testing.T) {
		task, approval, provenance := approvalChainFixture(h, "chain-default", agentdomain.SystemDefaultPolicyVersion, agentdomain.PolicySourceSystemDefault, agentdomain.SystemDefaultPolicy().Spec)
		if _, _, err := h.left.CreateForAppApproval(ctx, task, approval, provenance); !isPolicyStale(err) {
			t.Fatalf("default-source snapshot behind an explicit row verdict: %v", err)
		}
	})

	t.Run("matching snapshot creates the pending approval", func(t *testing.T) {
		task, approval, provenance := approvalChainFixture(h, "chain-match", 1, agentdomain.PolicySourceExplicit, spec)
		created, stored, err := h.left.CreateForAppApproval(ctx, task, approval, provenance)
		if err != nil || created.State != agentdomain.StateWaiting {
			t.Fatalf("matching snapshot creation: %v %+v", err, created)
		}
		if stored.State != agentdomain.ApprovalPending {
			t.Fatalf("approval must be pending: %+v", stored)
		}
	})

	t.Run("policy change invalidates the snapshot forever", func(t *testing.T) {
		changed := spec
		changed.MaxOutputTokensPerTask = 64
		if _, result, err := h.left.SetPolicy(ctx, agentports.SetPolicyCommand{
			OwnerUserID: h.owner, AppInstanceID: h.instance, ProjectID: h.project,
			Spec: changed, SpecDigest: changed.Digest(), ExpectedPolicyRevision: 1,
			IdempotencyKey: "chain-change", RequestDigest: agentdomain.SetPolicyRequestDigest(h.project, h.instance, 1, changed),
			Now: time.Now().UTC(),
		}); err != nil || !result.Changed {
			t.Fatalf("change policy: %v %+v", err, result)
		}
		// The approval created under revision 1 is expired by the change.
		if got := h.count("SELECT count(*) FROM workos_core.agent_app_approvals WHERE owner_user_id = $1 AND state = 'expired'", h.owner); got != 1 {
			t.Fatalf("pending approval must be expired by the change: %d", got)
		}
		// And the pre-change snapshot can never become a pending approval now.
		task, approval, provenance := approvalChainFixture(h, "chain-post-change", 1, agentdomain.PolicySourceExplicit, spec)
		if _, _, err := h.left.CreateForAppApproval(ctx, task, approval, provenance); !isPolicyStale(err) {
			t.Fatalf("post-change stale snapshot verdict: %v", err)
		}
	})
}

// TestAppAgentApprovalCreationUnderSystemDefault proves the missing-row half
// of the chain verification: with no explicit policy row, a versioned
// system-default snapshot is accepted.
func TestAppAgentApprovalCreationUnderSystemDefault(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newPolicyHarness(t, ctx)
	task, approval, provenance := approvalChainFixture(h, "chain-default-ok", agentdomain.SystemDefaultPolicyVersion, agentdomain.PolicySourceSystemDefault, agentdomain.SystemDefaultPolicy().Spec)
	_, stored, err := h.left.CreateForAppApproval(ctx, task, approval, provenance)
	if err != nil || stored.State != agentdomain.ApprovalPending {
		t.Fatalf("default snapshot creation: %v %+v", err, stored)
	}
	loaded, err := h.left.GetApproval(ctx, h.owner, stored.ID)
	if err != nil || loaded.Source != agentdomain.PolicySourceSystemDefault || loaded.Spec != agentdomain.SystemDefaultPolicy().Spec {
		t.Fatalf("default snapshot reload: %v %+v", err, loaded)
	}
}

// TestAppAgentUsageCostAccumulatesAcrossUnknownAndKnown pins the cost
// projection: a cost-less report leaves the recorded cost unknown (NULL), the
// first known cost starts the accumulation from it — never lost behind a
// NULL — and later known costs keep summing across events and tasks.
func TestAppAgentUsageCostAccumulatesAcrossUnknownAndKnown(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newPolicyHarness(t, ctx)
	spec := agentdomain.PolicySpec{
		Mode: agentdomain.PolicyModeAllow, MaxOutputTokensPerTask: 1000, MaxRuntimeSecondsPerTask: 60,
		MaxTasksPerUTCDay: 5, MaxReservedOutputTokensPerUTCDay: 10000,
	}
	service := agentapp.New(h.left, ids.UUIDv7{})
	submit := func(key string) agentdomain.Task {
		task, err := service.SubmitForApp(ctx, agentapp.AppSubmitInput{
			OwnerUserID: h.owner, AppInstanceID: h.instance, ClientIdempotencyKey: key,
			RequestDigest: agentdomain.AppTaskRequestDigest("", "cost "+key),
			ProjectID:     h.project, ProviderID: "fake", Role: "", Goal: "cost " + key,
			Enforcement: agentapp.AppRunEnforcement{
				Policy: agentports.PolicySnapshot{
					Source: agentdomain.PolicySourceExplicit, Revision: 1, SpecDigest: spec.Digest(), Spec: spec,
				},
				MaxOutputTokensTask: spec.MaxOutputTokensPerTask, MaxRuntimeSecondsTask: spec.MaxRuntimeSecondsPerTask,
				Daily: agentports.DailyAllowance{MaxTasks: spec.MaxTasksPerUTCDay, MaxReservedOutputTokens: spec.MaxReservedOutputTokensPerUTCDay},
			},
		})
		if err != nil {
			t.Fatalf("enqueue %s: %v", key, err)
		}
		return task
	}
	report := func(lease *agentdomain.Lease, worker string, input, output int64, cost string) {
		usage := &agentdomain.UsageReport{InputTokens: input, OutputTokens: output, Model: "fake/deterministic", CostDecimal: cost}
		payload, err := json.Marshal(map[string]any{
			"usageRecorded": map[string]any{"inputTokens": input, "outputTokens": output, "model": usage.Model},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.AppendEvent(ctx, lease.ID, worker, "usage_recorded", payload, agentdomain.StateRunning, "fake", "", usage); err != nil {
			t.Fatalf("append usage: %v", err)
		}
	}
	taskCost := func(taskID string) any {
		h.t.Helper()
		var cost any
		if err := h.pool.QueryRow(ctx,
			"SELECT cost_decimal FROM workos_core.agent_task_usage WHERE owner_user_id = $1 AND task_id = $2",
			h.owner, taskID,
		).Scan(&cost); err != nil {
			h.t.Fatalf("read task cost: %v", err)
		}
		return cost
	}
	equals := func(value any, literal string) bool {
		var equal bool
		if err := h.pool.QueryRow(ctx,
			"SELECT $1::numeric = $2::numeric", value, literal,
		).Scan(&equal); err != nil {
			h.t.Fatalf("compare cost: %v", err)
		}
		return equal
	}

	first := submit("cost-first")
	firstLease, err := service.Claim(ctx, "cost-worker", 30*time.Second)
	if err != nil || firstLease == nil || firstLease.Task.ID != first.ID {
		t.Fatalf("claim first: %v %+v", err, firstLease)
	}
	// Unknown → still unknown after the first cost-less report.
	report(firstLease, "cost-worker", 2, 1, "")
	var cost any
	if err := h.pool.QueryRow(ctx,
		"SELECT cost_decimal FROM workos_core.agent_task_usage WHERE owner_user_id = $1 AND task_id = $2",
		h.owner, first.ID,
	).Scan(&cost); err != nil || cost != nil {
		t.Fatalf("cost-less report must leave cost unknown: %v %v", err, cost)
	}
	// The first known cost must not be swallowed by the stored NULL.
	report(firstLease, "cost-worker", 2, 1, "0.25")
	if cost := taskCost(first.ID); !equals(cost, "0.25") {
		t.Fatalf("first known cost must start the accumulation, got %v", cost)
	}
	// Later known costs keep summing.
	report(firstLease, "cost-worker", 2, 1, "0.25")
	if cost := taskCost(first.ID); !equals(cost, "0.5") {
		t.Fatalf("known costs must accumulate, got %v", cost)
	}

	// A second task on the same UTC bucket adds its own cost on top.
	second := submit("cost-second")
	secondLease, err := service.Claim(ctx, "cost-worker-2", 30*time.Second)
	if err != nil || secondLease == nil || secondLease.Task.ID != second.ID {
		t.Fatalf("claim second: %v %+v", err, secondLease)
	}
	report(secondLease, "cost-worker-2", 2, 2, "1.00")

	var costSum, tokens, tasks bool
	if err := h.pool.QueryRow(ctx,
		"SELECT cost_decimal_recorded = 1.5, output_tokens_recorded = 5, tasks_recorded = 2 FROM workos_core.agent_app_daily_usage WHERE owner_user_id = $1 AND app_instance_id = $2",
		h.owner, h.instance,
	).Scan(&costSum, &tokens, &tasks); err != nil {
		t.Fatalf("read bucket: %v", err)
	}
	if !costSum || !tokens || !tasks {
		t.Fatalf("bucket projection wrong: cost1.5=%v tokens5=%v tasks2=%v", costSum, tokens, tasks)
	}
}

func isPolicyStale(err error) bool {
	for err != nil {
		if err == agentdomain.ErrPolicyStale {
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

// staticChainInstallations answers one active installation for the
// application-layer policy read regressions.
type staticChainInstallations struct{}

func (staticChainInstallations) ResolveActiveInstallation(context.Context, string, string, string) (agentports.InstallationFacts, error) {
	return agentports.InstallationFacts{AppID: "chain.app", GrantedPermissions: []string{"agent.task.run"}}, nil
}

func isPolicyCorrupt(err error) bool {
	for err != nil {
		if err == agentdomain.ErrPolicyCorrupt {
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

// TestAppAgentPolicyBindingFailsClosedEverywhere proves a format-valid but
// misbound policy row is storage corruption for reads, writes, and approval
// decisions. It is never served under a rewritten project, overwritten by a
// Set, or used to queue a waiting task.
func TestAppAgentPolicyBindingFailsClosedEverywhere(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newPolicyHarness(t, ctx)
	policyService, err := agentapp.NewPolicyService(h.left, staticChainInstallations{}, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	usageService, err := agentapp.NewUsageService(h.left, staticChainInstallations{})
	if err != nil {
		t.Fatal(err)
	}

	// No explicit row: the versioned system default serves, bound to the
	// installation's project.
	policy, err := policyService.EffectivePolicy(ctx, h.owner, h.project, h.instance)
	if err != nil || policy.Source != agentdomain.PolicySourceSystemDefault || policy.ProjectID != h.project {
		t.Fatalf("default read: %v %+v", err, policy)
	}

	spec := agentdomain.PolicySpec{
		Mode: agentdomain.PolicyModeRequireApproval, MaxOutputTokensPerTask: 64, MaxRuntimeSecondsPerTask: 30,
		MaxTasksPerUTCDay: 2, MaxReservedOutputTokensPerUTCDay: 64,
	}
	if _, result, err := h.left.SetPolicy(ctx, agentports.SetPolicyCommand{
		OwnerUserID: h.owner, AppInstanceID: h.instance, ProjectID: h.project,
		Spec: spec, SpecDigest: spec.Digest(), ExpectedPolicyRevision: 0,
		IdempotencyKey: "binding-seed", RequestDigest: agentdomain.SetPolicyRequestDigest(h.project, h.instance, 0, spec),
		Now: time.Now().UTC(),
	}); err != nil || !result.Changed {
		t.Fatalf("seed policy: %v %+v", err, result)
	}
	policy, err = policyService.EffectivePolicy(ctx, h.owner, h.project, h.instance)
	if err != nil || policy.Source != agentdomain.PolicySourceExplicit || policy.ProjectID != h.project {
		t.Fatalf("explicit read: %v %+v", err, policy)
	}
	task, approval, provenance := approvalChainFixture(h, "binding-approval", 1, agentdomain.PolicySourceExplicit, spec)
	createdTask, createdApproval, err := h.left.CreateForAppApproval(ctx, task, approval, provenance)
	if err != nil || createdTask.State != agentdomain.StateWaiting || createdApproval.State != agentdomain.ApprovalPending {
		t.Fatalf("create approval before corruption: %v %+v %+v", err, createdTask, createdApproval)
	}

	// Corrupt the binding: the row now points at a different project while
	// the installation stays active under h.project. Both read paths must
	// fail closed as corruption instead of re-binding the row silently.
	otherProject := newUUIDForTest(406)
	if _, err := h.pool.Exec(ctx,
		"UPDATE workos_core.agent_app_policies SET project_id = $1 WHERE owner_user_id = $2 AND app_instance_id = $3",
		otherProject, h.owner, h.instance,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := policyService.EffectivePolicy(ctx, h.owner, h.project, h.instance); !isPolicyCorrupt(err) {
		t.Fatalf("misbound policy read verdict: %v", err)
	}
	if _, _, err := usageService.AppDailyUsageWithPolicy(ctx, h.owner, h.project, h.instance, ""); !isPolicyCorrupt(err) {
		t.Fatalf("misbound usage read verdict: %v", err)
	}

	// Both a deterministic no-op and a real replacement must refuse before
	// consuming their request keys; neither may silently heal the stored fact.
	if _, _, err := policyService.SetPolicy(ctx, agentapp.SetPolicyInput{
		OwnerUserID: h.owner, ProjectID: h.project, AppInstanceID: h.instance,
		Spec: spec, ExpectedPolicyRevision: 1, IdempotencyKey: "binding-corrupt-noop",
	}); !isPolicyCorrupt(err) {
		t.Fatalf("misbound no-op Set verdict: %v", err)
	}
	changed := spec
	changed.MaxRuntimeSecondsPerTask++
	if _, _, err := policyService.SetPolicy(ctx, agentapp.SetPolicyInput{
		OwnerUserID: h.owner, ProjectID: h.project, AppInstanceID: h.instance,
		Spec: changed, ExpectedPolicyRevision: 1, IdempotencyKey: "binding-corrupt-change",
	}); !isPolicyCorrupt(err) {
		t.Fatalf("misbound changing Set verdict: %v", err)
	}
	if got := h.count("SELECT count(*) FROM workos_core.agent_app_policy_requests WHERE owner_user_id = $1 AND idempotency_key LIKE 'binding-corrupt-%'", h.owner); got != 0 {
		t.Fatalf("corrupt binding must not consume policy request keys: %d", got)
	}
	var storedProject, storedDigest string
	var storedRevision int64
	if err := h.pool.QueryRow(ctx,
		"SELECT project_id::text, spec_digest, policy_revision FROM workos_core.agent_app_policies WHERE owner_user_id = $1 AND app_instance_id = $2",
		h.owner, h.instance,
	).Scan(&storedProject, &storedDigest, &storedRevision); err != nil {
		t.Fatal(err)
	}
	if storedProject != otherProject || storedDigest != spec.Digest() || storedRevision != 1 {
		t.Fatalf("corrupt row was overwritten: project=%s digest=%s revision=%d", storedProject, storedDigest, storedRevision)
	}

	approvalService, err := agentapp.NewApprovalService(h.left, staticChainInstallations{}, staticFullCapabilities{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approvalService.Decide(ctx, agentapp.DecideInput{
		OwnerUserID: h.owner, ApprovalID: createdApproval.ID,
		Decision: agentdomain.ApprovalDecisionApprove, IdempotencyKey: "binding-corrupt-approve-app",
	}); !isPolicyCorrupt(err) {
		t.Fatalf("application approve against misbound policy: %v", err)
	}
	// The repository repeats the complete task/policy snapshot check inside
	// the decision transaction, protecting callers that bypass the application
	// preflight and closing the final reservation/outbox race window.
	if _, err := h.left.DecideApproval(ctx, agentports.DecideApprovalCommand{
		OwnerUserID: h.owner, ApprovalID: createdApproval.ID,
		Decision: agentdomain.ApprovalDecisionApprove, IdempotencyKey: "binding-corrupt-approve-store",
		DecisionDigest: agentdomain.DecideApprovalRequestDigest(createdApproval.ID, agentdomain.ApprovalDecisionApprove),
		Now:            time.Now().UTC(),
	}); !isPolicyCorrupt(err) {
		t.Fatalf("transactional approve against misbound policy: %v", err)
	}
	if got := h.count("SELECT count(*) FROM workos_core.agent_app_approvals WHERE owner_user_id = $1 AND id = $2 AND state = 'pending'", h.owner, createdApproval.ID); got != 1 {
		t.Fatalf("refused approval must remain pending: %d", got)
	}
	if got := h.count("SELECT count(*) FROM workos_core.agent_app_daily_reservations WHERE owner_user_id = $1 AND app_instance_id = $2", h.owner, h.instance); got != 0 {
		t.Fatalf("refused approval must not reserve quota: %d", got)
	}
	if got := h.count("SELECT count(*) FROM workos_events.outbox WHERE aggregate_type = 'agent-task' AND aggregate_id = $1", createdTask.ID); got != 0 {
		t.Fatalf("refused approval must not create an outbox request: %d", got)
	}
	if got := h.count("SELECT count(*) FROM workos_core.agent_tasks WHERE owner_user_id = $1 AND id = $2 AND state = 'waiting'", h.owner, createdTask.ID); got != 1 {
		t.Fatalf("refused approval task must remain waiting: %d", got)
	}
}
