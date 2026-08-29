package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// bridgeRepository is a focused session-store fake for the credential chain.
type bridgeRepository struct {
	mu       sync.Mutex
	sessions map[string]domain.SurfaceSession
	failWith error
}

func (r *bridgeRepository) put(session domain.SurfaceSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[session.ID] = session
}

func (r *bridgeRepository) LookupRequest(context.Context, string, string) (ports.StoredSessionRequest, bool, error) {
	return ports.StoredSessionRequest{}, false, nil
}

func (r *bridgeRepository) GetSession(_ context.Context, owner, device, id string) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[id]
	if !ok || session.OwnerUserID != owner || session.DeviceID != device {
		return domain.SurfaceSession{}, domain.ErrNotFound
	}
	return session, nil
}

func (r *bridgeRepository) GetActiveSession(_ context.Context, owner, device, id string, now time.Time) (domain.SurfaceSession, error) {
	session, err := r.GetSession(context.Background(), owner, device, id)
	if err != nil {
		return domain.SurfaceSession{}, err
	}
	if session.ClosedAt != nil || !session.ExpiresAt.After(now) {
		return domain.SurfaceSession{}, domain.ErrNotFound
	}
	return session, nil
}

func (r *bridgeRepository) Create(_ context.Context, command ports.CreateSessionCommand) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session := command.Session
	session.BridgeTokenHash = command.BridgeTokenHash
	r.sessions[session.ID] = session
	return session, nil
}

func (r *bridgeRepository) Close(_ context.Context, owner, device, id string, now time.Time) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[id]
	if !ok || session.OwnerUserID != owner || session.DeviceID != device {
		return domain.SurfaceSession{}, domain.ErrNotFound
	}
	if session.ClosedAt == nil {
		closed := now
		session.ClosedAt = &closed
		r.sessions[id] = session
	}
	return session, nil
}

func (r *bridgeRepository) RotateBridgeToken(context.Context, ports.RotateBridgeTokenCommand) (domain.SurfaceSession, error) {
	return domain.SurfaceSession{}, nil
}

func (r *bridgeRepository) GetActiveSessionByBridgeToken(_ context.Context, owner, tokenHash string, now time.Time) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return domain.SurfaceSession{}, r.failWith
	}
	for _, session := range r.sessions {
		if session.OwnerUserID != owner || session.BridgeTokenHash != tokenHash {
			continue
		}
		if session.ClosedAt != nil || !session.ExpiresAt.After(now) {
			continue
		}
		return session, nil
	}
	return domain.SurfaceSession{}, domain.ErrNotFound
}

type bridgeAppAgent struct {
	runResult    ports.AppTaskSubmission
	runErr       error
	watchErr     error
	queries      []ports.AppAgentRunQuery
	watchQueries []ports.AppAgentWatchQuery
}

func (a *bridgeAppAgent) RunAgentTask(_ context.Context, query ports.AppAgentRunQuery) (ports.AppTaskSubmission, error) {
	a.queries = append(a.queries, query)
	if a.runErr != nil {
		return ports.AppTaskSubmission{}, a.runErr
	}
	return a.runResult, nil
}

func (a *bridgeAppAgent) WatchAgentTaskEvents(_ context.Context, query ports.AppAgentWatchQuery, _ func(*agentv1.AgentEvent) error) error {
	a.watchQueries = append(a.watchQueries, query)
	return a.watchErr
}

func newBridgeTest(t *testing.T) (*BridgeService, *bridgeRepository, *bridgeAppAgent, string) {
	t.Helper()
	repository := &bridgeRepository{sessions: map[string]domain.SurfaceSession{}}
	appAgent := &bridgeAppAgent{runResult: ports.AppTaskSubmission{TaskID: "task-1", State: "queued"}}
	service, err := NewBridgeService(repository, appAgent)
	if err != nil {
		t.Fatal(err)
	}
	session := domain.SurfaceSession{
		ID: "0198d7ea-2110-7c42-b659-c5e4d73bc351", OwnerUserID: "owner-1", DeviceID: "device-1",
		ProjectID: "0198d7ea-2110-7c42-b659-c5e4d73bc352", AppInstanceID: "0198d7ea-2110-7c42-b659-c5e4d73bc353",
		BridgeCapabilities: []string{"agent.task.run", "agent.event.watch"},
		// A real persisted epoch — not 1 and not 0 — so forwarding assertions
		// prove the session snapshot is the source.
		InstallationGrantRevision: 9,
		ExpiresAt:                 time.Now().UTC().Add(time.Hour),
	}
	token, err := domain.NewBridgeToken()
	if err != nil {
		t.Fatal(err)
	}
	session.BridgeTokenHash = domain.HashBridgeToken(token)
	repository.put(session)
	return service, repository, appAgent, token
}

func TestBridgeRunAuthorizesAndDerivesScopeFromSession(t *testing.T) {
	service, _, appAgent, token := newBridgeTest(t)
	submission, err := service.RunAgentTask(context.Background(), "owner-1", "device-1", token, "client-key", "role", "goal")
	if err != nil || submission.TaskID != "task-1" {
		t.Fatalf("run failed: %+v %v", submission, err)
	}
	if len(appAgent.queries) != 1 {
		t.Fatalf("unexpected app agent calls: %d", len(appAgent.queries))
	}
	query := appAgent.queries[0]
	// The project/app instance come from the session facts, never the caller.
	if query.ProjectID != "0198d7ea-2110-7c42-b659-c5e4d73bc352" || query.AppInstanceID != "0198d7ea-2110-7c42-b659-c5e4d73bc353" {
		t.Fatalf("scope derivation failed: %+v", query)
	}
	if query.ClientKey != "client-key" || query.Goal != "goal" {
		t.Fatalf("bounded input not forwarded: %+v", query)
	}
}

// TestBridgeForwardsSessionGrantEpoch pins the private-call epoch source
// (ADR-0003 §7): run and watch queries carry exactly the validated session
// row's persisted InstallationGrantRevision. The public RunAgentTask /
// StreamAgentEvents signatures accept no revision-shaped input at all, so a
// bridge body or MessageChannel envelope structurally cannot influence the
// epoch Core compares.
func TestBridgeForwardsSessionGrantEpoch(t *testing.T) {
	service, _, appAgent, token := newBridgeTest(t)
	if _, err := service.RunAgentTask(context.Background(), "owner-1", "device-1", token, "client-key", "role", "goal"); err != nil {
		t.Fatal(err)
	}
	if len(appAgent.queries) != 1 || appAgent.queries[0].InstallationGrantRevision != 9 {
		t.Fatalf("run query epoch not derived from session: %+v", appAgent.queries)
	}
	if err := service.StreamAgentEvents(context.Background(), "owner-1", "device-1", token, "task-1", 0, func(*agentv1.AgentEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(appAgent.watchQueries) != 1 || appAgent.watchQueries[0].InstallationGrantRevision != 9 {
		t.Fatalf("watch query epoch not derived from session: %+v", appAgent.watchQueries)
	}
}

func TestBridgeCredentialChainFailsClosed(t *testing.T) {
	service, repository, _, token := newBridgeTest(t)

	if _, err := service.RunAgentTask(context.Background(), "owner-1", "device-1", "", "k", "", "goal"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("missing token verdict: %v", err)
	}
	if _, err := service.RunAgentTask(context.Background(), "owner-1", "device-1", "totally-invalid-token", "k", "", "goal"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("malformed token verdict: %v", err)
	}
	tampered, _ := domain.NewBridgeToken()
	if _, err := service.RunAgentTask(context.Background(), "owner-1", "device-1", tampered, "k", "", "goal"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("tampered token verdict: %v", err)
	}
	// A valid token from another trusted device is still a failed credential.
	if _, err := service.RunAgentTask(context.Background(), "owner-1", "device-2", token, "k", "", "goal"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("wrong device verdict: %v", err)
	}
	// Foreign owner: the lookup is owner-scoped.
	if _, err := service.RunAgentTask(context.Background(), "owner-2", "device-1", token, "k", "", "goal"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("foreign owner verdict: %v", err)
	}
	// Expired session: the store filters it, the verdict is the same.
	repository.mu.Lock()
	for id, session := range repository.sessions {
		session.ExpiresAt = time.Now().UTC().Add(-time.Minute)
		repository.sessions[id] = session
	}
	repository.mu.Unlock()
	if _, err := service.RunAgentTask(context.Background(), "owner-1", "device-1", token, "k", "", "goal"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expired session verdict: %v", err)
	}
}

func TestBridgeCapabilityGate(t *testing.T) {
	repository := &bridgeRepository{sessions: map[string]domain.SurfaceSession{}}
	appAgent := &bridgeAppAgent{runResult: ports.AppTaskSubmission{TaskID: "task-1"}}
	service, err := NewBridgeService(repository, appAgent)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := domain.NewBridgeToken()
	session := domain.SurfaceSession{
		ID: "0198d7ea-2110-7c42-b659-c5e4d73bc361", OwnerUserID: "owner-1", DeviceID: "device-1",
		BridgeCapabilities: []string{"agent.event.watch"}, // watch granted, run not
		ExpiresAt:          time.Now().UTC().Add(time.Hour),
	}
	session.BridgeTokenHash = domain.HashBridgeToken(token)
	repository.put(session)

	if _, err := service.RunAgentTask(context.Background(), "owner-1", "device-1", token, "k", "", "goal"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("run without grant verdict: %v", err)
	}
	if err := service.StreamAgentEvents(context.Background(), "owner-1", "device-1", token, "task-1", 0, func(*agentv1.AgentEvent) error { return nil }); err != nil {
		t.Fatalf("watch with grant failed: %v", err)
	}
}

func TestBridgeStoreOutageIsUnavailable(t *testing.T) {
	service, repository, _, token := newBridgeTest(t)
	repository.failWith = ports.ErrStoreUnavailable
	if _, err := service.RunAgentTask(context.Background(), "owner-1", "device-1", token, "k", "", "goal"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("store outage verdict: %v", err)
	}
}

func TestBridgeCoreDenialAndOutagePassThrough(t *testing.T) {
	service, _, appAgent, token := newBridgeTest(t)
	appAgent.runErr = ports.ErrAppAgentDenied
	if _, err := service.RunAgentTask(context.Background(), "owner-1", "device-1", token, "k", "", "goal"); !errors.Is(err, ports.ErrAppAgentDenied) {
		t.Fatalf("core denial verdict: %v", err)
	}
	appAgent.runErr = ports.ErrAppAgentUnavailable
	if _, err := service.RunAgentTask(context.Background(), "owner-1", "device-1", token, "k", "", "goal"); !errors.Is(err, ports.ErrAppAgentUnavailable) {
		t.Fatalf("core outage verdict: %v", err)
	}
}

func TestNewBridgeServiceRequiresDependencies(t *testing.T) {
	if _, err := NewBridgeService(nil, &bridgeAppAgent{}); err == nil {
		t.Fatal("nil repository accepted")
	}
	if _, err := NewBridgeService(&bridgeRepository{sessions: map[string]domain.SurfaceSession{}}, nil); err == nil {
		t.Fatal("nil app agent accepted")
	}
}

func TestBridgeTokenNeverAppearsInErrors(t *testing.T) {
	service, _, _, token := newBridgeTest(t)
	_, err := service.RunAgentTask(context.Background(), "owner-1", "device-1", token, "k", "", "goal")
	if err != nil && strings.Contains(err.Error(), token) {
		t.Fatal("token leaked into error")
	}
}
