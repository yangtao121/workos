//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/appregistry/adapters/manifestvalidator"
	registrydb "github.com/yangtao121/workos/internal/core/appregistry/adapters/postgres"
	registryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	registrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	registryports "github.com/yangtao121/workos/internal/core/appregistry/ports"
	"github.com/yangtao121/workos/internal/core/orchestration"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"github.com/yangtao121/workos/internal/platform/ids"
	"google.golang.org/protobuf/encoding/protojson"
)

func repairSourceFixture(t *testing.T) (*reviewFixture, *orchestration.RepairSources, registrydomain.SourceBundle) {
	t.Helper()
	f := newReviewFixture(t)
	repo := registrydb.New(f.pool)
	sources, err := registryapp.NewSourceService(repo, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := sources.Create(context.Background(), f.owner, "base", []registrydomain.SourceFile{{Path: "main.go", Content: []byte("package main\nfunc main() {}\n")}})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := manifestvalidator.New()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := registryapp.New(repo, validator, nil, nil, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	raw := repairSourceManifest(t, source, "1.0.0")
	if _, violations := validator.Validate(raw); len(violations) > 0 {
		t.Fatal(violations)
	}
	version, err := registry.Register(context.Background(), f.owner, "base-manifest", raw)
	if err != nil {
		t.Fatal(err)
	}
	input := &agentv1.AgentTaskInput{TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: f.project}}, IncidentId: ids.UUIDv7{}.New(), RepairTarget: &agentv1.RepairTarget{AppInstanceId: ids.UUIDv7{}.New(), AppId: version.AppID, Version: version.Version, ManifestDigest: version.ManifestDigest, ProjectRevision: 2}}
	payload, err := protojson.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	execScratch(t, f.pool, `UPDATE workos_core.agent_tasks SET input=$2 WHERE id=$1`, f.task, payload)
	service := newRepairSources(t, f, repo)
	return f, service, source
}
func newRepairSources(t *testing.T, f *reviewFixture, store registryports.BuildStore) *orchestration.RepairSources {
	t.Helper()
	validator, err := manifestvalidator.New()
	if err != nil {
		t.Fatal(err)
	}
	builds, err := registryapp.NewBuildService(store, validator, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := orchestration.NewRepairSources(f.pool, f.agentRepo, builds)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestRepairSourcesConcurrentIdentityAndRestart(t *testing.T) {
	f, service, base := repairSourceFixture(t)
	ctx := context.Background()
	input, err := service.Resolve(ctx, f.leaseID, f.worker)
	if err != nil || input.TaskID != f.task || input.Build.Source.Digest != base.Digest || !reflect.DeepEqual(input.Build.Recipe.TestCommand, []string{"go", "test", "./..."}) {
		t.Fatalf("resolve pinned input: %v", err)
	}
	type result struct {
		source registrydomain.SourceBundle
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 10)
	for i := range 10 {
		go func() {
			<-start
			task, source, err := service.Submit(ctx, f.leaseID, f.worker, []registrydomain.SourceFile{{Path: "main.go", Content: []byte(fmt.Sprintf("candidate-%d", i%2))}})
			if err == nil && task != f.task {
				err = errors.New("candidate task mismatch")
			}
			results <- result{source, err}
		}()
	}
	close(start)
	var winner registrydomain.SourceBundle
	accepted, conflicted := 0, 0
	for range 10 {
		r := <-results
		if errors.Is(r.err, registrydomain.ErrIdempotencyConflict) {
			conflicted++
			continue
		}
		if r.err != nil {
			t.Fatal(r.err)
		}
		accepted++
		if winner.ID == "" {
			winner = r.source
		} else if !reflect.DeepEqual(winner, r.source) {
			t.Fatal("different first response")
		}
	}
	if accepted != 5 || conflicted != 5 {
		t.Fatalf("accepted=%d conflicts=%d", accepted, conflicted)
	}
	restarted := newRepairSources(t, f, registrydb.New(f.pool))
	_, replayed, err := restarted.Submit(ctx, f.leaseID, f.worker, winner.Files)
	if err != nil || !reflect.DeepEqual(winner, replayed) {
		t.Fatalf("restart replay: %v", err)
	}
	input, err = restarted.Resolve(ctx, f.leaseID, f.worker)
	if err != nil || input.Build.Source.ID != base.ID {
		t.Fatalf("candidate replaced base input: %v", err)
	}
	for query, want := range map[string]int{`SELECT count(*) FROM workos_core.app_repair_source_candidates`: 1, `SELECT count(*) FROM workos_core.app_source_bundles`: 2, `SELECT count(*) FROM workos_core.app_versions`: 1} {
		var count int
		if err := f.pool.QueryRow(ctx, query).Scan(&count); err != nil || count != want {
			t.Fatalf("count=%d want=%d err=%v", count, want, err)
		}
	}
}

func TestRepairSourcesRejectLostAuthorityAndCorruptInputs(t *testing.T) {
	for _, scenario := range []string{"ordinary", "foreign worker", "cancelled", "expired", "terminal", "bad manifest", "bad source", "missing build"} {
		t.Run(scenario, func(t *testing.T) {
			f, service, base := repairSourceFixture(t)
			ctx := context.Background()
			worker := f.worker
			expected := agentdomain.ErrLeaseLost
			switch scenario {
			case "ordinary":
				execScratch(t, f.pool, `UPDATE workos_core.agent_tasks SET input=input-'repairTarget'-'incidentId' WHERE id=$1`, f.task)
				expected = agentdomain.ErrInvalid
			case "foreign worker":
				worker = "wrong-worker"
			case "cancelled":
				execScratch(t, f.pool, `UPDATE workos_core.agent_tasks SET cancellation_requested=true WHERE id=$1`, f.task)
			case "expired":
				execScratch(t, f.pool, `UPDATE workos_events.outbox SET locked_until=now()-interval '1 second' WHERE lease_id=$1`, f.leaseID)
			case "terminal":
				execScratch(t, f.pool, `UPDATE workos_core.agent_tasks SET state='completed' WHERE id=$1`, f.task)
				expected = agentdomain.ErrTerminal
			case "bad manifest":
				execScratch(t, f.pool, `UPDATE workos_core.app_versions SET canonical_manifest=jsonb_set(canonical_manifest,'{name}','"drift"')`)
				expected = registrydomain.ErrSourceCorrupt
			case "bad source":
				execScratch(t, f.pool, `UPDATE workos_core.app_source_bundles SET total_size_bytes=0 WHERE id=$1`, base.ID)
				expected = registrydomain.ErrSourceCorrupt
			case "missing build":
				// Re-pin a valid immutable manifest without a build recipe to model an App
				// that never declared rebuild support, rather than a corrupted digest.
				var raw []byte
				if err := f.pool.QueryRow(ctx, `SELECT canonical_manifest-'build' FROM workos_core.app_versions`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				validator, _ := manifestvalidator.New()
				manifest, violations := validator.Validate(raw)
				if len(violations) > 0 {
					t.Fatal(violations)
				}
				execScratch(t, f.pool, `UPDATE workos_core.app_versions SET canonical_manifest=$1,manifest_digest=$2`, manifest.CanonicalJSON, manifest.Digest)
				execScratch(t, f.pool, `UPDATE workos_core.agent_tasks SET input=jsonb_set(input,'{repairTarget,manifestDigest}',to_jsonb($2::text)) WHERE id=$1`, f.task, manifest.Digest)
				expected = registryapp.ErrBuildUnavailable
			}
			if _, err := service.Resolve(ctx, f.leaseID, worker); !errors.Is(err, expected) {
				t.Fatalf("resolve got %v want %v", err, expected)
			}
			if _, _, err := service.Submit(ctx, f.leaseID, worker, []registrydomain.SourceFile{{Path: "candidate", Content: []byte("changed")}}); !errors.Is(err, expected) {
				t.Fatalf("submit got %v want %v", err, expected)
			}
			var count int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM workos_core.app_repair_source_candidates`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("refusal persisted candidate: %d %v", count, err)
			}
		})
	}
}

type delayedBuildStore struct {
	registryports.BuildStore
	until time.Time
}

func (s delayedBuildStore) InsertRepairSource(ctx context.Context, tx dbtx.Tx, owner, taskID, sourceID string) error {
	if err := s.BuildStore.InsertRepairSource(ctx, tx, owner, taskID, sourceID); err != nil {
		return err
	}
	timer := time.NewTimer(time.Until(s.until))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func TestRepairSourcesExpiryBeforeCommitRollsBackAllWrites(t *testing.T) {
	f, _, _ := repairSourceFixture(t)
	ctx := context.Background()
	expiry := time.Now().UTC().Add(150 * time.Millisecond)
	execScratch(t, f.pool, `UPDATE workos_events.outbox SET locked_until=$2 WHERE lease_id=$1`, f.leaseID, expiry)
	service := newRepairSources(t, f, delayedBuildStore{registrydb.New(f.pool), expiry.Add(10 * time.Millisecond)})
	if _, _, err := service.Submit(ctx, f.leaseID, f.worker, []registrydomain.SourceFile{{Path: "candidate"}}); !errors.Is(err, agentdomain.ErrLeaseLost) {
		t.Fatalf("expired candidate commit: %v", err)
	}
	var sources, candidates int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM workos_core.app_source_bundles),(SELECT count(*) FROM workos_core.app_repair_source_candidates)`).Scan(&sources, &candidates); err != nil || sources != 1 || candidates != 0 {
		t.Fatalf("partial commit sources=%d candidates=%d err=%v", sources, candidates, err)
	}
}

func repairSourceManifest(t *testing.T, source registrydomain.SourceBundle, version string) []byte {
	t.Helper()
	manifest := map[string]any{
		"apiVersion": "workos.app/v1", "id": "repair-source-fixture", "name": "Repair source fixture", "version": version, "scope": "project",
		"runtime":  map[string]any{"type": "container", "image": "localhost/app@sha256:" + strings.Repeat("a", 64), "command": []string{"/app/app"}, "port": 8080},
		"surfaces": []any{map[string]any{"id": "main", "renderer": "web-service", "route": "/"}}, "permissions": []string{},
		"resources": map[string]any{"cpuHard": 1, "memoryHighMb": 64, "memoryMaxMb": 96, "pidsMax": 32},
		"health":    map[string]any{"httpPath": "/health", "startupSeconds": 10, "restartLimit": 1}, "maintainer": map[string]any{},
		"build": map[string]any{"sourceBundleId": source.ID, "sourceDigest": source.Digest, "baseImage": "localhost/toolchain@sha256:" + strings.Repeat("b", 64), "buildCommand": []string{"go", "build", "."}, "testCommand": []string{"go", "test", "./..."}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
