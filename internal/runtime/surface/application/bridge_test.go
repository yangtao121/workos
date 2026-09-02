package application

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	notificationv1 "github.com/yangtao121/workos/gen/go/workos/notification/v1"
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
	runResult      ports.AppTaskSubmission
	runErr         error
	watchErr       error
	queries        []ports.AppAgentRunQuery
	watchQueries   []ports.AppAgentWatchQuery
	authorizeOwner string
	authorizeDeny  bool
	authorizeCalls int
	notifications  []*notificationv1.CreateAppNotificationResponse
	createErr      error
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
	service, err := NewBridgeService(repository, appAgent, nil)
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
	service, err := NewBridgeService(repository, appAgent, nil)
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
	if _, err := NewBridgeService(nil, &bridgeAppAgent{}, nil); err == nil {
		t.Fatal("nil repository accepted")
	}
	if _, err := NewBridgeService(&bridgeRepository{sessions: map[string]domain.SurfaceSession{}}, nil, nil); err == nil {
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

func (r *bridgeRepository) HasActiveSurface(context.Context, string, string, time.Time) (bool, error) {
	return false, nil
}

func (a *bridgeAppAgent) AuthorizeAppKnowledge(_ context.Context, query ports.AppKnowledgeAuthQuery) (ports.AppKnowledgeBinding, error) {
	a.authorizeCalls++
	if a.authorizeDeny {
		return ports.AppKnowledgeBinding{}, ports.ErrAppAgentDenied
	}
	return ports.AppKnowledgeBinding{OwnerUserID: a.authorizeOwner, ProjectID: query.ProjectID}, nil
}

// TestBridgeKnowledgeSearchOrder proves the ADR-0013 fixed order: the local
// capability gate runs first (nil pipeline or missing capability never touch
// Core), then Core re-authorization, and only a successful binding reaches
// the indexer. Denials provably leave the indexer call count at zero.
func TestBridgeKnowledgeSearchOrder(t *testing.T) {
	validToken := base64.RawURLEncoding.EncodeToString(make([]byte, 32)) // valid grammar
	hash := domain.HashBridgeToken(validToken)
	baseSession := func(capabilities ...string) domain.SurfaceSession {
		return domain.SurfaceSession{
			ID: "01999999-9999-7999-8999-0000000000a1", OwnerUserID: "01999999-9999-7999-8999-0000000000b1",
			DeviceID: "01999999-9999-7999-8999-0000000000c1", ProjectID: "01999999-9999-7999-8999-0000000000d1",
			AppInstanceID: "01999999-9999-7999-8999-0000000000e1", InstallationGrantRevision: 2,
			BridgeTokenHash: hash, BridgeCapabilities: capabilities,
			ExpiresAt: time.Now().Add(time.Hour),
		}
	}
	query := ports.KnowledgeSearchQuery{Query: "unique"}
	_ = query

	t.Run("nil pipeline denies without touching Core", func(t *testing.T) {
		repo := &bridgeRepository{sessions: map[string]domain.SurfaceSession{}}
		appAgent := &bridgeAppAgent{}
		service, err := NewBridgeService(repo, appAgent, nil)
		if err != nil {
			t.Fatal(err)
		}
		repo.put(baseSession("knowledge.search"))
		if _, err := service.SearchKnowledge(context.Background(), "01999999-9999-7999-8999-0000000000b1",
			"01999999-9999-7999-8999-0000000000c1", validToken, "query", 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("nil pipeline = %v, want PermissionDenied", err)
		}
		if appAgent.authorizeCalls != 0 {
			t.Fatalf("nil pipeline called Core %d times", appAgent.authorizeCalls)
		}
	})

	t.Run("Core denial reaches the indexer zero times", func(t *testing.T) {
		repo := &bridgeRepository{sessions: map[string]domain.SurfaceSession{}}
		appAgent := &bridgeAppAgent{authorizeDeny: true}
		indexer := &recordingKnowledgeSearch{}
		service, err := NewBridgeService(repo, appAgent, mustPipeline(t, appAgent, indexer))
		if err != nil {
			t.Fatal(err)
		}
		repo.put(baseSession("knowledge.search"))
		if _, err := service.SearchKnowledge(context.Background(), "01999999-9999-7999-8999-0000000000b1",
			"01999999-9999-7999-8999-0000000000c1", validToken, "query", 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("Core denial = %v, want PermissionDenied", err)
		}
		if indexer.calls != 0 {
			t.Fatalf("denied call reached the indexer %d times", indexer.calls)
		}
	})

	t.Run("capability gate precedes Core", func(t *testing.T) {
		repo := &bridgeRepository{sessions: map[string]domain.SurfaceSession{}}
		appAgent := &bridgeAppAgent{authorizeOwner: "01999999-9999-7999-8999-0000000000b1"}
		indexer := &recordingKnowledgeSearch{page: ports.KnowledgeSearchPage{NextPageToken: ""}}
		service, err := NewBridgeService(repo, appAgent, mustPipeline(t, appAgent, indexer))
		if err != nil {
			t.Fatal(err)
		}
		repo.put(baseSession("agent.task.run"))
		if _, err := service.SearchKnowledge(context.Background(), "01999999-9999-7999-8999-0000000000b1",
			"01999999-9999-7999-8999-0000000000c1", validToken, "query", 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("missing capability = %v, want PermissionDenied", err)
		}
		if appAgent.authorizeCalls != 0 || indexer.calls != 0 {
			t.Fatalf("capability gate leaked: core=%d indexer=%d", appAgent.authorizeCalls, indexer.calls)
		}
	})

	t.Run("malformed search input precedes Core and indexer", func(t *testing.T) {
		repo := &bridgeRepository{sessions: map[string]domain.SurfaceSession{}}
		appAgent := &bridgeAppAgent{}
		indexer := &recordingKnowledgeSearch{}
		service, err := NewBridgeService(repo, appAgent, mustPipeline(t, appAgent, indexer))
		if err != nil {
			t.Fatal(err)
		}
		repo.put(baseSession("knowledge.search"))
		for _, input := range []struct{ query, token string }{
			{query: "query\nwith-control"},
			{query: "   "},
			{query: "query", token: "not+base64url"},
			{query: "query", token: strings.Repeat("a", maxKnowledgePageTokenBytes+1)},
		} {
			if _, err := service.SearchKnowledge(context.Background(), "01999999-9999-7999-8999-0000000000b1",
				"01999999-9999-7999-8999-0000000000c1", validToken, input.query, 0, input.token); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("malformed input error = %v", err)
			}
		}
		if appAgent.authorizeCalls != 0 || indexer.calls != 0 {
			t.Fatalf("malformed input leaked: core=%d indexer=%d", appAgent.authorizeCalls, indexer.calls)
		}
	})

	t.Run("binding mismatch fails closed", func(t *testing.T) {
		repo := &bridgeRepository{sessions: map[string]domain.SurfaceSession{}}
		appAgent := &bridgeAppAgent{authorizeOwner: "someone-else"}
		indexer := &recordingKnowledgeSearch{}
		service, err := NewBridgeService(repo, appAgent, mustPipeline(t, appAgent, indexer))
		if err != nil {
			t.Fatal(err)
		}
		repo.put(baseSession("knowledge.search"))
		if _, err := service.SearchKnowledge(context.Background(), "01999999-9999-7999-8999-0000000000b1",
			"01999999-9999-7999-8999-0000000000c1", validToken, "query", 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("binding mismatch = %v, want PermissionDenied", err)
		}
		if indexer.calls != 0 {
			t.Fatalf("mismatched binding reached the indexer %d times", indexer.calls)
		}
	})

	t.Run("happy path calls the indexer once with the derived scope", func(t *testing.T) {
		repo := &bridgeRepository{sessions: map[string]domain.SurfaceSession{}}
		appAgent := &bridgeAppAgent{authorizeOwner: "01999999-9999-7999-8999-0000000000b1"}
		indexer := &recordingKnowledgeSearch{page: ports.KnowledgeSearchPage{
			Hits: []ports.KnowledgeHit{{
				ArtifactID: "01999999-9999-7999-8999-0000000000f1", Digest: "sha256:" + strings.Repeat("ab", 32),
				ArtifactType: "document.markdown.v1", Title: "Doc", Excerpt: "unique", Score: 1,
			}},
		}}
		service, err := NewBridgeService(repo, appAgent, mustPipeline(t, appAgent, indexer))
		if err != nil {
			t.Fatal(err)
		}
		repo.put(baseSession("knowledge.search"))
		page, err := service.SearchKnowledge(context.Background(), "01999999-9999-7999-8999-0000000000b1",
			"01999999-9999-7999-8999-0000000000c1", validToken, "query", 0, "")
		if err != nil {
			t.Fatal(err)
		}
		if indexer.calls != 1 || indexer.queries[0].OwnerUserID != "01999999-9999-7999-8999-0000000000b1" ||
			indexer.queries[0].ProjectID != "01999999-9999-7999-8999-0000000000d1" || indexer.queries[0].PageSize != 20 {
			t.Fatalf("indexer calls %d queries %+v", indexer.calls, indexer.queries)
		}
		if len(page.Hits) != 1 || page.Hits[0].ArtifactID != "01999999-9999-7999-8999-0000000000f1" {
			t.Fatalf("projected hits: %+v", page.Hits)
		}
	})
}

func mustPipeline(t *testing.T, authorizer ports.AppAgentClient, indexer ports.KnowledgeSearchClient) *KnowledgeSearchPipeline {
	t.Helper()
	pipeline, err := NewKnowledgeSearchPipeline(authorizer, indexer)
	if err != nil {
		t.Fatal(err)
	}
	return pipeline
}

type recordingKnowledgeSearch struct {
	calls   int
	queries []ports.KnowledgeSearchQuery
	page    ports.KnowledgeSearchPage
}

func (r *recordingKnowledgeSearch) Search(_ context.Context, query ports.KnowledgeSearchQuery) (ports.KnowledgeSearchPage, error) {
	r.calls++
	r.queries = append(r.queries, query)
	return r.page, nil
}

func (a *bridgeAppAgent) CreateAppNotification(_ context.Context, query ports.AppNotificationCreateQuery) (*notificationv1.CreateAppNotificationResponse, error) {
	if a.createErr != nil {
		return nil, a.createErr
	}
	return &notificationv1.CreateAppNotificationResponse{}, nil
}
