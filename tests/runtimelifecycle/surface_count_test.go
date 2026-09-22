package runtimelifecycle_test

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
	surfacepostgres "github.com/yangtao121/workos/internal/runtime/surface/adapters/postgres"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
	workloadpostgres "github.com/yangtao121/workos/internal/runtime/workload/adapters/postgres"
	workloaddomain "github.com/yangtao121/workos/internal/runtime/workload/domain"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAppDeviceCountUsesOnlyLiveExactGenerationViews(t *testing.T) {
	ctx := context.Background()
	url := os.Getenv("WORKOS_LIFECYCLE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires WORKOS_LIFECYCLE_TEST_DATABASE_URL")
	}
	if err := migrations.Run(ctx, url); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	continuity := surfacepostgres.NewContinuity(pool)
	repository := surfacepostgres.New(pool)
	generator := ids.UUIDv7{}
	owner, project, workload, instance, deviceA, deviceB := generator.New(), generator.New(), generator.New(), generator.New(), generator.New(), generator.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	workloads, err := workloadpostgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	requested := workloaddomain.RequestedPolicy{CPUHardCores: 1, MemoryHighMB: 64, MemoryMaxMB: 96, PidsMax: 32, HTTPPath: "/health", StartupSeconds: 10, RestartLimit: 3}
	row := workloaddomain.Workload{ID: workload, OwnerUserID: owner, ProjectID: project, AppInstanceID: instance, AppID: "count-fixture", AppVersion: "1.0.0", ManifestDigest: "sha256:" + strings.Repeat("b", 64), Image: "fixture@sha256:" + strings.Repeat("a", 64), Command: []string{"/fixture"}, Port: 8080, Requested: requested, Effective: workloaddomain.EffectiveFromRequested(requested), Generation: 3, State: workloaddomain.StateStarting, HealthVerdict: workloaddomain.HealthUnknown, LastExit: workloaddomain.ExitNone, CreatedAt: now, UpdatedAt: now, LifecycleMode: 2, ContainerName: workloaddomain.ContainerName(workload)}
	operation := workloaddomain.WorkloadOperation{WorkloadID: workload, OperationKey: generator.New(), Operation: workloaddomain.OperationEnsure, RequestDigest: workloaddomain.OperationDigest(workloaddomain.OperationEnsure, workload, row.Image, row.Command, row.Port, requested, 2), ResultGeneration: 3, CreatedAt: now, UpdatedAt: now}
	if _, err := workloads.ReserveEnsure(ctx, row, operation); err != nil {
		t.Fatal(err)
	}

	for i, device := range []string{deviceA, deviceA, deviceB, deviceB} {
		generation := int64(3)
		if i == 3 {
			generation = 2
		}
		id := generator.New()
		session := domain.SurfaceSession{ID: id, OwnerUserID: owner, DeviceID: device, IdempotencyKey: id, RequestDigest: "sha256:" + strings.Repeat("a", 64), ProjectID: project, AppInstanceID: instance, Renderer: domain.RendererWebService, Descriptor: domain.LaunchDescriptor{AppID: "count-fixture", Version: "1.0.0", ManifestDigest: "sha256:" + strings.Repeat("b", 64)}, WorkloadID: workload, WorkloadGeneration: generation, Path: domain.SessionPath(id), InstallationGrantRevision: 1, CreatedAt: now, ExpiresAt: now.Add(time.Minute), LifecycleMode: 2}
		stored, err := repository.Create(ctx, ports.CreateSessionCommand{Session: session, IdempotencyKey: id, RequestDigest: session.RequestDigest})
		if err != nil {
			t.Fatal(err)
		}
		if stored.LifecycleMode != 2 {
			t.Fatal("surface row lost actual program policy")
		}
	}
	count, err := continuity.CountAppDevices(ctx, owner, project, workload, 3, now)
	if err != nil || count != 2 {
		t.Fatalf("count: %d %v", count, err)
	}
	count, err = continuity.CountAppDevices(ctx, owner, project, workload, 4, now)
	if err != nil || count != 0 {
		t.Fatalf("old generation counted as current: %d %v", count, err)
	}
	count, err = continuity.CountAppDevices(ctx, owner, project, workload, 3, now.Add(2*time.Minute))
	if err != nil || count != 0 {
		t.Fatalf("expired access counted: %d %v", count, err)
	}
}
