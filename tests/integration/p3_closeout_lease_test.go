//go:build integration && repairbuildtest && p3delivery

package integration_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/internal/platform/ids"
	artifactfiles "github.com/yangtao121/workos/internal/runtime/artifactstore/adapters/files"
	artifactpg "github.com/yangtao121/workos/internal/runtime/artifactstore/adapters/postgres"
	artifactapp "github.com/yangtao121/workos/internal/runtime/artifactstore/application"
	"github.com/yangtao121/workos/internal/runtime/buildtest/adapters/dockerbuild"
	buildpg "github.com/yangtao121/workos/internal/runtime/buildtest/adapters/postgres"
	buildapp "github.com/yangtao121/workos/internal/runtime/buildtest/application"
	builddomain "github.com/yangtao121/workos/internal/runtime/buildtest/domain"
)

// This independent executor shares the actual Runtime DB and bundle store.
// Restrict its poll to this test's task so it cannot claim other fixtures.
type p3TaskJobStore struct {
	*buildpg.Repository
	task string
}

func (s p3TaskJobStore) ListRunnable(ctx context.Context, _ int, now time.Time) ([]builddomain.Job, error) {
	job, err := s.GetJobByTask(ctx, s.task)
	if err != nil {
		return nil, err
	}
	if job.State == builddomain.StateQueued || (job.State == builddomain.StateRunning && job.LeaseUntil != nil && job.LeaseUntil.Before(now)) {
		return []builddomain.Job{job}, nil
	}
	return nil, nil
}

func p3CompetingWorker(t *testing.T, clients *buildtestClients, taskID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("WORKOS_REPAIR_BUILDTEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := buildpg.New(pool)
	first, err := repo.GetJobByTask(ctx, taskID)
	if err != nil || first.LeaseOwner == "" {
		t.Fatalf("first holder missing: %+v %v", first, err)
	}
	for first.LeaseUntil != nil && !time.Now().After(*first.LeaseUntil) {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if renewed, err := repo.RenewJobLease(ctx, first.ID, first.LeaseOwner, time.Now().Add(time.Minute), time.Now()); err != nil || renewed {
		t.Fatalf("expired holder renewed: %v %v", renewed, err)
	}
	root := os.Getenv("WORKOS_P3_GATE_DIR")
	files, err := artifactfiles.New(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := dockerbuild.New(dockerbuild.Config{Socket: "/var/run/docker.sock"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := buildapp.NewService(p3TaskJobStore{Repository: repo, task: taskID}, engine,
		artifactapp.New(artifactpg.New(pool), files), ids.UUIDv7{}, "p3-second-worker",
		filepath.Join(root, "buildtest", "second-worker"), time.Minute, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := service.RunPass(ctx, time.Now().UTC()); err != nil || count != 1 {
		t.Fatalf("independent executor did not take over: count=%d err=%v", count, err)
	}
	winner, err := repo.GetJobByTask(ctx, taskID)
	if err != nil || winner.State != builddomain.StateSucceeded || winner.Attempts != first.Attempts+1 || winner.ArtifactID == "" {
		t.Fatalf("handoff must persist exactly one successful artifact: %+v %v", winner, err)
	}
	// Deliver the old token's verdict after the real winner committed.
	if err := repo.RecordVerdict(ctx, first.ID, first.LeaseOwner, winner); !errors.Is(err, builddomain.ErrNotFound) {
		t.Fatalf("late loser overwrote the winning verdict: %v", err)
	}
	artifact, err := clients.builds.GetBuildArtifact(ctx, connect.NewRequest(&executionv1.GetBuildArtifactRequest{TaskId: taskID}))
	if err != nil || artifact.Msg.GetArtifact().GetState() != "ready" || artifact.Msg.GetArtifact().GetArtifactId() != winner.ArtifactID || artifact.Msg.GetArtifact().GetArtifactDigest() != winner.ArtifactDigest {
		t.Fatalf("winner's authoritative artifact mismatch: %v %v", artifact, err)
	}
	t.Logf("two real workers: job=%s attempts=%d artifact=%s digest=%s; stale renew/verdict refused", winner.ID, winner.Attempts, winner.ArtifactID, winner.ArtifactDigest)
}
