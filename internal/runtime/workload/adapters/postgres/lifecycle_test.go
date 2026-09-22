package postgres

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInstalledLifecycleRoundtripAndRestart(t *testing.T) {
	url := os.Getenv("WORKOS_LIFECYCLE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires WORKOS_LIFECYCLE_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	if err := migrations.Run(ctx, url); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	generator := ids.UUIDv7{}
	now := time.Now().UTC().Truncate(time.Microsecond)
	policy := domain.RequestedPolicy{CPUHardCores: 1, MemoryHighMB: 64, MemoryMaxMB: 96, PidsMax: 32, HTTPPath: "/health", StartupSeconds: 10, RestartLimit: 3}
	row := domain.Workload{ID: generator.New(), OwnerUserID: generator.New(), ProjectID: generator.New(), AppInstanceID: generator.New(), AppID: "lifecycle-fixture", AppVersion: "1.0.0", ManifestDigest: "sha256:" + strings.Repeat("a", 64), Image: "fixture@sha256:" + strings.Repeat("b", 64), Command: []string{"/fixture"}, Port: 8080, Requested: policy, Effective: domain.EffectiveFromRequested(policy), Generation: 1, State: domain.StateStarting, HealthVerdict: domain.HealthUnknown, LastExit: domain.ExitNone, CreatedAt: now, UpdatedAt: now, LifecycleMode: 2}
	row.ContainerName = domain.ContainerName(row.ID)
	operation := domain.WorkloadOperation{WorkloadID: row.ID, OperationKey: generator.New(), Operation: domain.OperationEnsure, RequestDigest: domain.OperationDigest(domain.OperationEnsure, row.ID, row.Image, row.Command, row.Port, row.Requested, row.LifecycleMode), ResultGeneration: 1, CreatedAt: now, UpdatedAt: now}
	if ok, err := repository.ReserveEnsure(ctx, row, operation); err != nil || !ok {
		t.Fatalf("reserve: %v %v", ok, err)
	}
	stored, err := repository.Get(ctx, row.ID)
	if err != nil || stored.LifecycleMode != 2 {
		t.Fatalf("manual mode not persisted: %#v %v", stored, err)
	}
	listed, err := repository.ListProject(ctx, row.OwnerUserID, row.ProjectID)
	if err != nil || len(listed) != 1 || listed[0].ID != row.ID {
		t.Fatalf("scoped listing: %#v %v", listed, err)
	}
	if err := repository.Transition(ctx, row.ID, domain.StateStarting, domain.StateStopped, ports.WorkloadFacts{Generation: 1, HealthVerdict: domain.HealthUnknown, LastExit: domain.ExitNone, StoppedAt: &now, ClearEngine: true}, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.Transition(ctx, row.ID, domain.StateStopped, domain.StateStarting, ports.WorkloadFacts{Generation: 2, RestartCount: 1, LifecycleMode: 1, HealthVerdict: domain.HealthUnknown, LastExit: domain.ExitNone, ClearEngine: true}, now); err != nil {
		t.Fatal(err)
	}
	stored, err = repository.Get(ctx, row.ID)
	if err != nil || stored.Generation != 2 || stored.LifecycleMode != 1 {
		t.Fatalf("restart mode not persisted: %#v %v", stored, err)
	}
}
