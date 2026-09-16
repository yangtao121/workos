//go:build integration

package integration_test

import (
	"connectrpc.com/connect"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	workloadv1 "github.com/yangtao121/workos/gen/go/workos/workload/v1"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	notificationpostgres "github.com/yangtao121/workos/internal/core/notification/adapters/postgres"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectpostgres "github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	"github.com/yangtao121/workos/internal/core/project/adapters/postgres/projectdb"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
	"google.golang.org/protobuf/types/known/structpb"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type authorityRuntime struct {
	calls   atomic.Int32
	started chan struct{}
}

func (r *authorityRuntime) ExecuteWorkspaceOperation(ctx context.Context, req *connect.Request[workloadv1.ExecuteWorkspaceOperationRequest]) (*connect.Response[workloadv1.ExecuteWorkspaceOperationResponse], error) {
	r.calls.Add(1)
	if req.Msg.Operation == "shell.run" {
		close(r.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	value, _ := structpb.NewStruct(map[string]any{"source": req.Msg.WorkspaceSourceId, "readOnly": req.Msg.ReadOnly})
	return connect.NewResponse(&workloadv1.ExecuteWorkspaceOperationResponse{Result: value}), nil
}
func TestSessionToolAuthorizationAndRevocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	gen := ids.UUIDv7{}
	owner := gen.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.users(id,kind,display_name,created_at) VALUES($1,'owner','tool fixture',now())`, owner); err != nil {
		t.Fatal(err)
	}
	projects := projectapp.New(projectpostgres.New(pool), gen)
	project, err := projects.Create(ctx, projectapp.CreateInput{OwnerUserID: owner, IdempotencyKey: "project", Name: "Tool fixture"})
	if err != nil {
		t.Fatal(err)
	}
	bindings := projectpostgres.NewWorkspaceRepository(projectdb.New(pool))
	now := time.Now().UTC()
	binding := projectdomain.WorkspaceBinding{ID: gen.New(), OwnerUserID: owner, ProjectID: project.ID, WorkspaceSourceID: "source-fixture", IdempotencyKey: "binding", DisplayName: "Fixture", State: projectdomain.WorkspaceBindingActive, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err := bindings.InsertWorkspaceBinding(ctx, binding, "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	tasks, err := agentpostgres.NewWithNotificationSink(pool, notificationpostgres.New(pool))
	if err != nil {
		t.Fatal(err)
	}
	sessions := agentpostgres.NewSessionRepository(pool)
	session := agentdomain.Session{ID: gen.New(), OwnerUserID: owner, ProjectID: project.ID, IdempotencyKey: "session", WorkspaceBindingID: binding.ID, WorkspaceBindingRevision: 1, ProviderID: "fake", CreatedAt: now, UpdatedAt: now}
	if _, err := sessions.InsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"goal": "fixture", "agentSessionId": session.ID, "targetScope": map[string]any{"projectId": project.ID}})
	admitted, err := tasks.Create(ctx, agentdomain.Task{ID: gen.New(), OwnerUserID: owner, ProjectID: project.ID, Input: input, State: agentdomain.StateQueued, ProviderID: "fake", CreatedAt: now, UpdatedAt: now}, "tool-task")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workos_core.agent_sessions SET active_task_id=$1 WHERE session_id=$2`, admitted.ID, session.ID); err != nil {
		t.Fatal(err)
	}
	// Real claim eligibility requires the committed session-input association.
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.agent_session_inputs(input_id,session_id,owner_user_id,client_input_id,input_text,request_digest,state,task_id,sequence,created_at,updated_at) VALUES($1,$2,$3,'input','fixture',$4,'dispatched',$5,1,now(),now())`, gen.New(), session.ID, owner, agentdomain.InputRequestDigest("input", "fixture"), admitted.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := tasks.Claim(ctx, "worker", 20*time.Second, gen.New(), now)
	if err != nil || lease == nil {
		t.Fatalf("claim: %v %v", lease, err)
	}
	runtime := &authorityRuntime{started: make(chan struct{})}
	route, handler := workloadv1connect.NewWorkspaceExecutionServiceHandler(runtime)
	mux := http.NewServeMux()
	mux.Handle(route, handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	tools := &orchestration.SessionTools{Pool: pool, Tasks: tasks, Sessions: sessions, Projects: projects, Workspaces: projectapp.NewWorkspaceService(bindings, nil, gen), Runtime: workloadv1connect.NewWorkspaceExecutionServiceClient(server.Client(), server.URL)}
	if _, err := tools.Execute(ctx, lease.ID, "foreign-worker", gen.New(), "fs.read", map[string]any{}); err == nil {
		t.Fatal("foreign worker authorized")
	}
	result, err := tools.Execute(ctx, lease.ID, "worker", gen.New(), "fs.read", map[string]any{"path": "file.txt", "ownerUserId": gen.New(), "workspaceSourceId": "foreign"})
	if err != nil || result["source"] != binding.WorkspaceSourceID {
		t.Fatalf("scope=%v %v", result, err)
	}

	interactions := agentapp.NewInteractionService(tasks, tools, gen)
	tools.Interactions = interactions
	askResult := make(chan map[string]any, 1)
	askError := make(chan error, 1)
	go func() {
		result, err := tools.Execute(ctx, lease.ID, "worker", gen.New(), "interaction.ask", map[string]any{"requestKey": "question", "questions": []any{map[string]any{"id": "choice", "text": "Choose fixture", "choices": []any{map[string]any{"label": "Continue"}}}}})
		askResult <- result
		askError <- err
	}()
	var pending agentdomain.ExecutionInteraction
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := interactions.List(ctx, owner, admitted.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 {
			pending = rows[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pending.ID == "" {
		t.Fatal("question not persisted")
	}
	answers := []agentdomain.ExecutionAnswer{{QuestionID: "choice", Selected: []string{"Continue"}}}
	if _, err := interactions.Respond(ctx, gen.New(), pending.ID, "foreign", false, answers); err == nil {
		t.Fatal("foreign answer accepted")
	}
	if _, err := interactions.Respond(ctx, owner, pending.ID, "invalid", false, []agentdomain.ExecutionAnswer{{QuestionID: "choice", Selected: []string{"not-offered"}}}); err == nil {
		t.Fatal("unoffered choice accepted")
	}
	if _, err := interactions.Respond(ctx, owner, pending.ID, "answer", false, answers); err != nil {
		t.Fatal(err)
	}
	if _, err := interactions.Respond(ctx, owner, pending.ID, "answer", false, answers); err != nil {
		t.Fatal("same decision did not replay", err)
	}
	if _, err := interactions.Respond(ctx, owner, pending.ID, "different", true, nil); err == nil {
		t.Fatal("late contradictory decision accepted")
	}
	select {
	case result := <-askResult:
		if result["state"] != "answered" {
			t.Fatal(result)
		}
		if err := <-askError; err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native tool wait did not resolve")
	}

	for _, state := range []string{"reject", "expired"} {
		now := time.Now().UTC()
		expires := now.Add(time.Minute)
		if state == "expired" {
			expires = now.Add(-time.Second)
		}
		pending, err := tasks.CreateInteraction(ctx, agentdomain.ExecutionInteraction{ID: gen.New(), TaskID: admitted.ID, OwnerUserID: owner, ProjectID: project.ID, LeaseID: lease.ID, WorkerID: "worker", RequestKey: state, Questions: []agentdomain.ExecutionQuestion{{ID: "choice", Text: "Confirm fixture"}}, State: "pending", CreatedAt: now, ExpiresAt: expires})
		if err != nil {
			t.Fatal(err)
		}
		result, err := interactions.Respond(ctx, owner, pending.ID, "decision", true, nil)
		if state == "reject" && (err != nil || result.State != "rejected") {
			t.Fatal("reject failed", err)
		}
		if state == "expired" && err == nil {
			t.Fatal("expired question accepted decision")
		}
	}
	finished := make(chan error, 1)
	go func() {
		_, err := tools.Execute(ctx, lease.ID, "worker", gen.New(), "shell.run", map[string]any{"command": "sleep 60"})
		finished <- err
	}()
	select {
	case <-runtime.started:
	case <-ctx.Done():
		t.Fatal("command never reached runtime")
	}
	if _, err := bindings.UpdateWorkspaceAccess(ctx, owner, binding.ID, true, 1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("revoked command completed successfully")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("revision change did not abort running command")
	}
	if _, err := tools.Execute(ctx, lease.ID, "worker", gen.New(), "fs.read", map[string]any{}); err == nil {
		t.Fatal("stale workspace revision authorized")
	}
	if runtime.calls.Load() != 2 {
		t.Fatalf("denied operations reached runtime: %d", runtime.calls.Load())
	}
}
