//go:build integration && continuitygate

package integration_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
)

func surfaceGateEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv("WORKOS_SURFACE_GATE_" + name)
	if value == "" {
		t.Fatalf("run through tools/surface-continuity/gate.sh (missing %s)", name)
	}
	return value
}

// readUntilMarker polls the bounded read cursor until the shell echoed the
// marker or the deadline passes; it returns everything seen so far.
func readUntilMarker(ctx context.Context, client surfacev1connect.PtySessionServiceClient,
	owner, device, sessionID string, marker string, deadline time.Time) (string, bool) {
	var cursor int64
	var output []byte
	for time.Now().Before(deadline) {
		read, err := client.ReadPtySession(ctx, continuityRequest(&surfacev1.ReadPtySessionRequest{
			SessionId: sessionID, After: cursor, MaxBytes: 65536,
		}, owner, device))
		if err != nil {
			return string(output), false
		}
		output = append(output, read.Msg.GetOutput()...)
		cursor = int64(read.Msg.GetCursor())
		if strings.Contains(string(output), marker) {
			return string(output), true
		}
		select {
		case <-ctx.Done():
			return string(output), false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return string(output), false
}

// TestSurfaceContinuity proves ADR-0031's B06/B07 chain on a real stack:
// detach keeps the program running (output keeps accumulating while no
// device is attached), a re-attach plus explicit takeover resumes control,
// stop deterministically reaps, session workloads are honestly not
// restartable, the single-controller lease is enforced on the PTY data path
// for two independent device identities, and the bounded attachment sweep
// expires elapsed controllers.
// continuityRequest carries the trusted identity headers: through the
// dev-bypass gateway they are re-injected from configuration; on the
// runtime's private listener they are exactly the pair the production
// gateway derives from the device session.
func continuityRequest[Req any](body *Req, owner, device string) *connect.Request[Req] {
	request := connect.NewRequest(body)
	request.Header().Set(identity.UserHeader, owner)
	request.Header().Set(identity.DeviceHeader, device)
	return request
}

func TestSurfaceContinuity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 30 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	gatewayURL := surfaceGateEnv(t, "GATEWAY_URL")
	runtimeURL := surfaceGateEnv(t, "RUNTIME_URL")
	databaseURL := surfaceGateEnv(t, "DATABASE_URL")

	pty := surfacev1connect.NewPtySessionServiceClient(client, gatewayURL)
	continuity := surfacev1connect.NewSurfaceContinuityServiceClient(client, gatewayURL)
	projects := projectv1connect.NewProjectServiceClient(client, gatewayURL)
	// Identity headers are trusted only on the runtime's private listener;
	// the dev-bypass gateway pins one fixed device, so the two-device control
	// takeover proof runs directly against the runtime — the exact identity
	// pair the production gateway injects there.
	directPty := surfacev1connect.NewPtySessionServiceClient(client, runtimeURL)
	directContinuity := surfacev1connect.NewSurfaceContinuityServiceClient(client, runtimeURL)

	owner := "01999999-9999-7999-8999-000000000b01"
	gatewayDevice := "01999999-9999-7999-8999-000000000b02"
	deviceA := "01999999-9999-7999-8999-000000000c01"
	deviceB := "01999999-9999-7999-8999-000000000c02"

	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: fmt.Sprintf("continuity-%d", time.Now().UnixNano()), Name: "Continuity Fixture",
	}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	projectID := created.Msg.GetProject().GetId()

	// (a) The program keeps running across detach; only stop reaps it.
	session, err := pty.CreatePtySession(ctx, continuityRequest(&surfacev1.CreatePtySessionRequest{
		IdempotencyKey: fmt.Sprintf("surface-continuity-%d", time.Now().UnixNano()),
		ProjectId:      projectID, Columns: 90, Rows: 26,
	}, owner, gatewayDevice))
	if err != nil || session.Msg.GetSession().GetState() != "running" {
		t.Fatalf("create pty: %v %+v", err, session.Msg.GetSession())
	}
	workloadID := session.Msg.GetSession().GetId()
	defer func() {
		_, _ = pty.ClosePtySession(context.Background(), continuityRequest(&surfacev1.ClosePtySessionRequest{SessionId: workloadID}, owner, gatewayDevice))
	}()

	listed, err := continuity.ListProjectSurfaces(ctx, continuityRequest(&surfacev1.ListProjectSurfacesRequest{ProjectId: projectID}, owner, gatewayDevice))
	if err != nil || len(listed.Msg.GetWorkloads()) != 1 {
		t.Fatalf("list project surfaces: %v %d", err, len(listed.Msg.GetWorkloads()))
	}
	view := listed.Msg.GetWorkloads()[0]
	if view.GetWorkloadId() != workloadID || view.GetState() != "running" || view.GetDisplayName() == "" {
		t.Fatalf("workload view facts drifted: %+v", view)
	}
	if !view.GetPolicy().GetPersistent() || view.GetPolicy().GetKeepAliveSeconds() != 1800 {
		t.Fatalf("bounded policy must be the honest 30-minute ceiling: %+v", view.GetPolicy())
	}

	attached, err := continuity.AttachSurface(ctx, continuityRequest(&surfacev1.AttachSurfaceRequest{
		WorkloadId: workloadID, IdempotencyKey: "attach-first",
	}, owner, gatewayDevice))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if attached.Msg.GetAttachment().GetControls() != true || attached.Msg.GetAttachment().GetControlGeneration() != 1 {
		t.Fatalf("first attach must grant control generation 1: %+v", attached.Msg.GetAttachment())
	}
	if attached.Msg.GetSession().GetId() != workloadID {
		t.Fatalf("attach must return the existing session facts, got %q", attached.Msg.GetSession().GetId())
	}

	// A command whose second half only appears after the detach: the program
	// keeps executing with no device attached.
	markerEarly := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("early-%d", time.Now().UnixNano())))
	markerLate := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("late-%d", time.Now().UnixNano())))
	if _, err := pty.WritePtySession(ctx, continuityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: workloadID,
		Input:     []byte("echo workos-" + markerEarly + "; sleep 2; echo workos-" + markerLate + "\n"),
	}, owner, gatewayDevice)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if output, ok := readUntilMarker(ctx, pty, owner, gatewayDevice, workloadID, markerEarly, time.Now().Add(20*time.Second)); !ok {
		t.Fatalf("early marker missing: %q", output)
	}

	if _, err := continuity.DetachSurface(ctx, continuityRequest(&surfacev1.DetachSurfaceRequest{
		SurfaceSessionId: workloadID,
	}, owner, gatewayDevice)); err != nil {
		t.Fatalf("detach: %v", err)
	}

	// Output keeps accumulating while detached: the late marker appears
	// although this device holds no live attachment.
	if output, ok := readUntilMarker(ctx, pty, owner, gatewayDevice, workloadID, markerLate, time.Now().Add(20*time.Second)); !ok {
		t.Fatalf("program stopped after detach; late marker missing: %q", output)
	}
	control, err := continuity.GetSurfaceControl(ctx, continuityRequest(&surfacev1.GetSurfaceControlRequest{WorkloadId: workloadID}, owner, gatewayDevice))
	if err != nil || !control.Msg.GetWorkloadRunning() {
		t.Fatalf("workload must still run after detach: %v %+v", err, control.Msg)
	}

	// A second attach alone does not restore input (the detached controller's
	// lease still governs); the explicit takeover does.
	reattached, err := continuity.AttachSurface(ctx, continuityRequest(&surfacev1.AttachSurfaceRequest{
		WorkloadId: workloadID, IdempotencyKey: "attach-second",
	}, owner, gatewayDevice))
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
	if reattached.Msg.GetAttachment().GetControls() {
		t.Fatal("attach must never grant control over a live lease")
	}
	if _, err := pty.WritePtySession(ctx, continuityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: workloadID, Input: []byte("echo must-not-run\n"),
	}, owner, gatewayDevice)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("write without control must be denied, got: %v", err)
	}
	takeover, err := continuity.RequestSurfaceControl(ctx, continuityRequest(&surfacev1.RequestSurfaceControlRequest{
		SurfaceSessionId: workloadID,
	}, owner, gatewayDevice))
	if err != nil {
		t.Fatalf("request control: %v", err)
	}
	if takeover.Msg.GetAttachment().GetControlGeneration() != 2 || !takeover.Msg.GetAttachment().GetControls() {
		t.Fatalf("takeover must advance to generation 2: %+v", takeover.Msg.GetAttachment())
	}
	resumeMarker := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("resume-%d", time.Now().UnixNano())))
	if _, err := pty.WritePtySession(ctx, continuityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: workloadID, Input: []byte("echo workos-" + resumeMarker + "\n"),
	}, owner, gatewayDevice)); err != nil {
		t.Fatalf("write after takeover: %v", err)
	}
	if output, ok := readUntilMarker(ctx, pty, owner, gatewayDevice, workloadID, resumeMarker, time.Now().Add(20*time.Second)); !ok {
		t.Fatalf("resume marker missing: %q", output)
	}

	// Session workloads are honestly not restartable.
	if _, err := continuity.RestartSurfaceWorkload(ctx, continuityRequest(&surfacev1.RestartSurfaceWorkloadRequest{
		WorkloadId: workloadID, ActionKey: "restart-1",
	}, owner, gatewayDevice)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("restart must be refused honestly: %v", err)
	}

	// (b) Two independent device identities: single-controller enforcement.
	twoDevices, err := directPty.CreatePtySession(ctx, continuityRequest(&surfacev1.CreatePtySessionRequest{
		IdempotencyKey: fmt.Sprintf("surface-control-%d", time.Now().UnixNano()),
		ProjectId:      projectID, Columns: 90, Rows: 26,
	}, owner, deviceA))
	if err != nil {
		t.Fatalf("create pty (two devices): %v", err)
	}
	twoWorkload := twoDevices.Msg.GetSession().GetId()
	defer func() {
		_, _ = directPty.ClosePtySession(context.Background(), continuityRequest(&surfacev1.ClosePtySessionRequest{SessionId: twoWorkload}, owner, deviceA))
	}()

	attachA, err := directContinuity.AttachSurface(ctx, continuityRequest(&surfacev1.AttachSurfaceRequest{
		WorkloadId: twoWorkload, IdempotencyKey: "control-a",
	}, owner, deviceA))
	if err != nil || !attachA.Msg.GetAttachment().GetControls() {
		t.Fatalf("device A attach+control: %v %+v", err, attachA.Msg.GetAttachment())
	}
	attachB, err := directContinuity.AttachSurface(ctx, continuityRequest(&surfacev1.AttachSurfaceRequest{
		WorkloadId: twoWorkload, IdempotencyKey: "control-b",
	}, owner, deviceB))
	if err != nil || attachB.Msg.GetAttachment().GetControls() {
		t.Fatalf("device B attach must be observer: %v %+v", err, attachB.Msg.GetAttachment())
	}
	if _, err := directPty.WritePtySession(ctx, continuityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: twoWorkload, Input: []byte("echo from-b\n"),
	}, owner, deviceB)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("non-controller write must be PermissionDenied, got: %v", err)
	}
	if _, err := directPty.ResizePtySession(ctx, continuityRequest(&surfacev1.ResizePtySessionRequest{
		SessionId: twoWorkload, Columns: 100, Rows: 30,
	}, owner, deviceB)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("non-controller resize must be PermissionDenied, got: %v", err)
	}
	if _, err := directPty.WritePtySession(ctx, continuityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: twoWorkload, Input: []byte("echo from-a\n"),
	}, owner, deviceA)); err != nil {
		t.Fatalf("controller write must pass: %v", err)
	}

	// B takes over explicitly; A's input dies immediately, and A's late
	// re-attach (a new access relation) must not steal control back.
	if _, err := directContinuity.RequestSurfaceControl(ctx, continuityRequest(&surfacev1.RequestSurfaceControlRequest{
		SurfaceSessionId: twoWorkload,
	}, owner, deviceB)); err != nil {
		t.Fatalf("device B takeover: %v", err)
	}
	markerB := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("from-b-%d", time.Now().UnixNano())))
	if _, err := directPty.WritePtySession(ctx, continuityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: twoWorkload, Input: []byte("echo workos-" + markerB + "\n"),
	}, owner, deviceB)); err != nil {
		t.Fatalf("write after B takeover: %v", err)
	}
	if output, ok := readUntilMarker(ctx, directPty, owner, deviceA, twoWorkload, markerB, time.Now().Add(20*time.Second)); !ok {
		t.Fatalf("B's marker missing (reads stay owner-scoped): %q", output)
	}
	if _, err := directPty.WritePtySession(ctx, continuityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: twoWorkload, Input: []byte("echo late-a\n"),
	}, owner, deviceA)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("superseded A write must be denied, got: %v", err)
	}
	if _, err := directContinuity.AttachSurface(ctx, continuityRequest(&surfacev1.AttachSurfaceRequest{
		WorkloadId: twoWorkload, IdempotencyKey: "control-a-2",
	}, owner, deviceA)); err != nil {
		t.Fatalf("A re-attach: %v", err)
	}
	if _, err := directPty.WritePtySession(ctx, continuityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: twoWorkload, Input: []byte("echo steal-a\n"),
	}, owner, deviceA)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("A re-attach must not steal control: %v", err)
	}
	facts, err := directContinuity.GetSurfaceControl(ctx, continuityRequest(&surfacev1.GetSurfaceControlRequest{WorkloadId: twoWorkload}, owner, deviceA))
	if err != nil || facts.Msg.GetControllerDeviceId() != deviceB || facts.Msg.GetControlGeneration() != 2 {
		t.Fatalf("control facts must still name B at generation 2: %v %+v", err, facts.Msg)
	}

	// (c) The bounded sweep expires an elapsed controller attachment: force
	// the stored expiry into the past, then wait for the runtime's 30s
	// maintenance pass and observe the live attachment count drop.
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	if _, err := conn.Exec(ctx,
		`UPDATE workos_runtime.surface_attachments SET control_expires_at = now() - interval '1 second' WHERE workload_id = $1 AND controls`, twoWorkload); err != nil {
		t.Fatalf("age controller expiry: %v", err)
	}
	_ = conn.Close(ctx)
	sweepDeadline := time.Now().Add(45 * time.Second)
	for {
		after, err := directContinuity.ListProjectSurfaces(ctx, continuityRequest(&surfacev1.ListProjectSurfacesRequest{ProjectId: projectID}, owner, deviceA))
		if err != nil {
			t.Fatalf("list after sweep: %v", err)
		}
		count := int32(0)
		for _, entry := range after.Msg.GetWorkloads() {
			if entry.GetWorkloadId() == twoWorkload {
				count = entry.GetAttachmentCount()
			}
		}
		// Three attachments exist for this workload: A's first (its own
		// generation-1 expiry is still in the future), B's controller row
		// (forced into the past), and A's re-attachment (observer, no
		// control expiry). The sweep must expire exactly B's row: count 2.
		if count == 2 {
			break
		}
		if time.Now().After(sweepDeadline) {
			t.Fatalf("attachment sweep did not expire the elapsed controller (count=%d)", count)
		}
		select {
		case <-ctx.Done():
			t.Fatal("context ended waiting for the sweep")
		case <-time.After(2 * time.Second):
		}
	}
	// An expired epoch denies the former controller until an explicit takeover.
	if _, err := directPty.WritePtySession(ctx, continuityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: twoWorkload, Input: []byte("echo expired-b\n"),
	}, owner, deviceB)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("expired controller write must be denied, got: %v", err)
	}
	if _, err := directContinuity.RequestSurfaceControl(ctx, continuityRequest(&surfacev1.RequestSurfaceControlRequest{
		SurfaceSessionId: twoWorkload,
	}, owner, deviceA)); err != nil {
		t.Fatalf("takeover after expiry: %v", err)
	}
	if _, err := directPty.WritePtySession(ctx, continuityRequest(&surfacev1.WritePtySessionRequest{
		SessionId: twoWorkload, Input: []byte("echo reclaimed-a\n"),
	}, owner, deviceA)); err != nil {
		t.Fatalf("write after reclaim: %v", err)
	}

	// Stop deterministically reaps both programs and expires attachments.
	for _, stop := range []struct {
		id, key string
	}{{workloadID, "stop-main"}, {twoWorkload, "stop-two"}} {
		stopped, err := directContinuity.StopSurfaceWorkload(ctx, continuityRequest(&surfacev1.StopSurfaceWorkloadRequest{
			WorkloadId: stop.id, ActionKey: stop.key,
		}, owner, deviceA))
		if err != nil {
			t.Fatalf("stop %s: %v", stop.id, err)
		}
		if stopped.Msg.GetWorkload().GetState() == "running" {
			t.Fatalf("stop must report the terminal state: %+v", stopped.Msg.GetWorkload())
		}
		if _, err := directPty.WritePtySession(ctx, continuityRequest(&surfacev1.WritePtySessionRequest{
			SessionId: stop.id, Input: []byte("echo stopped\n"),
		}, owner, deviceA)); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("write after stop must be NotFound, got: %v", err)
		}
	}
	// Attaching a stopped workload reports the true state instead of starting it.
	if _, err := directContinuity.AttachSurface(ctx, continuityRequest(&surfacev1.AttachSurfaceRequest{
		WorkloadId: workloadID, IdempotencyKey: "attach-after-stop",
	}, owner, deviceA)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("attach after stop must be FailedPrecondition with the true state, got: %v", err)
	}
}

var _ = identity.UserHeader

// TestSurfaceContinuityRestartReconcile proves A13: a previously-live session
// whose process died with the runtime-host container is finalized honestly
// at startup — after the gate stops and restarts the runtime, the workload is
// no longer listed as running, control facts report not-running, attach
// fails FailedPrecondition with the true state, and the dead session's IO
// path is NotFound. The gate drives the two phases through
// WORKOS_SURFACE_GATE_RESTART_PHASE (prepare creates and pins the workload,
// verify asserts the reconciled facts); without the flag the test skips.
func TestSurfaceContinuityRestartReconcile(t *testing.T) {
	phase := os.Getenv("WORKOS_SURFACE_GATE_RESTART_PHASE")
	if phase == "" {
		t.Skip("run through tools/surface-continuity/gate.sh restart phase (WORKOS_SURFACE_GATE_RESTART_PHASE)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 30 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	gatewayURL := surfaceGateEnv(t, "GATEWAY_URL")

	pty := surfacev1connect.NewPtySessionServiceClient(client, gatewayURL)
	continuity := surfacev1connect.NewSurfaceContinuityServiceClient(client, gatewayURL)
	projects := projectv1connect.NewProjectServiceClient(client, gatewayURL)

	owner := "01999999-9999-7999-8999-000000000b01"
	gatewayDevice := "01999999-9999-7999-8999-000000000b02"

	switch phase {
	case "prepare":
		created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
			IdempotencyKey: fmt.Sprintf("continuity-restart-%d", time.Now().UnixNano()), Name: "Continuity Restart Fixture",
		}))
		if err != nil {
			t.Fatalf("create project: %v", err)
		}
		projectID := created.Msg.GetProject().GetId()
		session, err := pty.CreatePtySession(ctx, continuityRequest(&surfacev1.CreatePtySessionRequest{
			IdempotencyKey: fmt.Sprintf("surface-restart-%d", time.Now().UnixNano()),
			ProjectId:      projectID, Columns: 90, Rows: 26,
		}, owner, gatewayDevice))
		if err != nil || session.Msg.GetSession().GetState() != "running" {
			t.Fatalf("create pty: %v %+v", err, session.Msg.GetSession())
		}
		workloadID := session.Msg.GetSession().GetId()
		listed, err := continuity.ListProjectSurfaces(ctx, continuityRequest(&surfacev1.ListProjectSurfacesRequest{ProjectId: projectID}, owner, gatewayDevice))
		if err != nil {
			t.Fatalf("list before restart: %v", err)
		}
		found := false
		for _, entry := range listed.Msg.GetWorkloads() {
			if entry.GetWorkloadId() == workloadID {
				found = entry.GetState() == "running"
			}
		}
		if !found {
			t.Fatalf("workload %s not listed running before the restart", workloadID)
		}
		attached, err := continuity.AttachSurface(ctx, continuityRequest(&surfacev1.AttachSurfaceRequest{
			WorkloadId: workloadID, IdempotencyKey: "restart-attach",
		}, owner, gatewayDevice))
		if err != nil || !attached.Msg.GetAttachment().GetControls() || attached.Msg.GetAttachment().GetControlGeneration() != 1 {
			t.Fatalf("attach before restart: %v %+v", err, attached.Msg.GetAttachment())
		}
		// The session is deliberately left running: its process must die with
		// the runtime container the gate stops next. No close/stop cleanup.
		fmt.Printf("WORKOS_SURFACE_GATE_RESTART_WORKLOAD=%s\n", workloadID)
		fmt.Printf("WORKOS_SURFACE_GATE_RESTART_PROJECT=%s\n", projectID)
	case "verify":
		workloadID := surfaceGateEnv(t, "RESTART_WORKLOAD")
		projectID := surfaceGateEnv(t, "RESTART_PROJECT")
		listed, err := continuity.ListProjectSurfaces(ctx, continuityRequest(&surfacev1.ListProjectSurfacesRequest{ProjectId: projectID}, owner, gatewayDevice))
		if err != nil {
			t.Fatalf("list after restart: %v", err)
		}
		for _, entry := range listed.Msg.GetWorkloads() {
			if entry.GetWorkloadId() == workloadID {
				t.Fatalf("workload %s whose process died with the runtime is still listed (state %s)", workloadID, entry.GetState())
			}
		}
		control, err := continuity.GetSurfaceControl(ctx, continuityRequest(&surfacev1.GetSurfaceControlRequest{WorkloadId: workloadID}, owner, gatewayDevice))
		if err != nil {
			t.Fatalf("control facts after restart: %v", err)
		}
		if control.Msg.GetWorkloadRunning() {
			t.Fatal("control facts still claim the dead workload is running")
		}
		if _, err := continuity.AttachSurface(ctx, continuityRequest(&surfacev1.AttachSurfaceRequest{
			WorkloadId: workloadID, IdempotencyKey: "restart-attach-after",
		}, owner, gatewayDevice)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("attach after the runtime restart must fail with the honest terminal state, got: %v", err)
		}
		if _, err := pty.WritePtySession(ctx, continuityRequest(&surfacev1.WritePtySessionRequest{
			SessionId: workloadID, Input: []byte("echo dead\n"),
		}, owner, gatewayDevice)); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("write to the dead session must be NotFound, got: %v", err)
		}
	default:
		t.Fatalf("unknown WORKOS_SURFACE_GATE_RESTART_PHASE %q", phase)
	}
}
