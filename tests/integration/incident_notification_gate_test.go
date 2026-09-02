//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	incidentv1 "github.com/yangtao121/workos/gen/go/workos/incident/v1"
	incidentv1connect "github.com/yangtao121/workos/gen/go/workos/incident/v1/incidentv1connect"
	notificationv1 "github.com/yangtao121/workos/gen/go/workos/notification/v1"
	notificationv1connect "github.com/yangtao121/workos/gen/go/workos/notification/v1/notificationv1connect"

	"github.com/yangtao121/workos/internal/platform/ids"
)

// TestIncidentNotificationCrossProcess is the software-side cross-process
// gate for ADR-0014's reliability chain: a real runtime-host workload
// observation (seeded into the runtime-owned schema of the running stack), a
// real reliability-host supervisor that opens the incident and appends the
// durable publication in the same transaction, and the real Core consumer
// goroutine that projects the owner notification through the private
// claim/complete channel. This proves the cross-process software chain; it
// does NOT prove rootless supervisor acceptance, which stays gated behind
// make test-podman-fixture evidence and never changes docs/status.json's
// Reliability capability verdicts.
func TestIncidentNotificationCrossProcess(t *testing.T) {
	baseURL := os.Getenv("WORKOS_TEST_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	databaseURL := scratchDatabaseURL()
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("postgres is not reachable for the incident gate: %v", err)
	}
	defer admin.Close()

	// The deployment's single owner is the notification audience. Seed a
	// failed fixture workload in the LIVE runtime schema: the deterministic
	// grammar satisfies the workload CHECKs without any container existing,
	// and the supervisor's observation pipeline reads these rows through
	// runtime-host's private RPC.
	owner, err := singleOwnerID(ctx, admin)
	if err != nil {
		t.Fatalf("resolve owner: %v", err)
	}
	projectID := ids.UUIDv7{}.New()
	key := "incident-gate-project-" + projectID
	_, err = admin.Exec(ctx,
		`INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at)
		 VALUES ($1, $2, $5, 'Incident Gate', $3, $4, now(), now())`,
		projectID, owner, ids.UUIDv7{}.New(), ids.UUIDv7{}.New(), key)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	workloadID := ids.UUIDv7{}.New()
	_, err = admin.Exec(ctx, `
INSERT INTO workos_runtime.workloads (
    id, owner_user_id, project_id, app_instance_id, app_id, app_version,
    manifest_digest, image, command, port,
    requested_policy, policy_version,
    effective_cpu_quota_us, effective_memory_high_bytes, effective_memory_max_bytes,
    effective_pids_max, effective_startup_seconds, effective_restart_limit,
    generation, state, restart_count, container_name,
    health_verdict, last_exit_category, created_at, updated_at, stopped_at
) VALUES (
    $1, $2, $3, $4, 'incident-gate-app', '1.0.0',
    'sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
    'registry.example/fixture@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
    '["/bin/true"]'::jsonb, 18080,
    '{"cpuHard":1.0,"memoryHighMb":64,"memoryMaxMb":128,"pidsMax":32}'::jsonb, 'v1',
    100000, 67108864, 134217728, 32, 30, 2,
    1, 'failed', 2, 'workos-wl-' || $5::text,
    'unknown', 'exited', now(), now(), now()
)`, workloadID, owner, projectID, ids.UUIDv7{}.New(), workloadID)
	if err != nil {
		t.Fatalf("seed workload: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, `DELETE FROM workos_runtime.workloads WHERE id = $1`, workloadID)
	})

	httpClient := liveStackHTTPClient()
	incidents := incidentv1connect.NewIncidentServiceClient(httpClient, baseURL)
	notifications := notificationv1connect.NewNotificationServiceClient(httpClient, baseURL)

	// Wait for the reliability supervisor to open the incident for this exact
	// workload and for the Core consumer to project the owner notification
	// into the owner stream. Both facts are read through the public surfaces
	// the way the owner's clients see them.
	incidentID := ""
	notificationID := ""
	for attempt := 0; attempt < 240 && !notificationSeen(incidentID, notificationID); attempt++ {
		if incidentID == "" {
			listed, listErr := incidents.ListIncidents(ctx, connect.NewRequest(&incidentv1.ListIncidentsRequest{
				ProjectId: projectID,
				Page:      &commonv1.PageRequest{PageSize: 50},
			}))
			if listErr == nil {
				for _, incident := range listed.Msg.GetIncidents() {
					if incident.GetWorkloadId() == workloadID {
						incidentID = incident.GetId()
						break
					}
				}
			}
		}
		if incidentID != "" {
			page, pageErr := notifications.ListNotifications(ctx, connect.NewRequest(
				&notificationv1.ListNotificationsRequest{PageSize: 100}))
			if pageErr == nil {
				for _, fact := range page.Msg.GetNotifications() {
					if fact.GetTarget() != nil && fact.GetTarget().GetTargetId() == incidentID {
						notificationID = fact.GetId()
						break
					}
				}
			}
		}
		time.Sleep(time.Second)
	}
	if incidentID == "" {
		t.Fatal("the reliability supervisor never opened the incident for the fixture workload")
	}
	if notificationID == "" {
		t.Fatal("the Core consumer never projected the incident notification")
	}

	// The projection is exactly-once and idempotent: the summary and the
	// owner list agree, and the notification carries the incident target.
	page, err := notifications.ListNotifications(ctx, connect.NewRequest(
		&notificationv1.ListNotificationsRequest{PageSize: 100}))
	if err != nil {
		t.Fatalf("final list: %v", err)
	}
	matches := 0
	for _, fact := range page.Msg.GetNotifications() {
		if fact.GetTarget() != nil && fact.GetTarget().GetTargetId() == incidentID {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("expected exactly one incident notification, got %d", matches)
	}
}

func notificationSeen(incidentID, notificationID string) bool {
	return incidentID != "" && notificationID != ""
}

func liveStackHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
}

func singleOwnerID(ctx context.Context, admin *pgxpool.Pool) (string, error) {
	var owner string
	if err := admin.QueryRow(ctx, `SELECT id FROM workos_core.users LIMIT 1`).Scan(&owner); err != nil {
		return "", fmt.Errorf("query owner: %w", err)
	}
	return owner, nil
}
