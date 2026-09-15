package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

type memoryStore struct {
	mu       sync.Mutex
	sessions map[string]domain.Session
	byKey    map[string]string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{sessions: map[string]domain.Session{}, byKey: map[string]string{}}
}

func (m *memoryStore) InsertSession(_ context.Context, session domain.Session) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := session.OwnerUserID + "/" + session.IdempotencyKey
	if id, ok := m.byKey[key]; ok {
		return m.sessions[id].RequestDigest, false, nil
	}
	m.sessions[session.SessionID] = session
	m.byKey[key] = session.SessionID
	return session.RequestDigest, true, nil
}

func (m *memoryStore) GetSession(_ context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[sessionID]
	if !ok || session.OwnerUserID != ownerUserID {
		return domain.Session{}, domain.ErrNotFound
	}
	return session, nil
}

func (m *memoryStore) GetSessionByKey(_ context.Context, ownerUserID, idempotencyKey string) (domain.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byKey[ownerUserID+"/"+idempotencyKey]
	if !ok {
		return domain.Session{}, domain.ErrNotFound
	}
	return m.sessions[id], nil
}

func (m *memoryStore) UpdateState(_ context.Context, ownerUserID, sessionID string, state domain.State, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[sessionID]
	if !ok || session.OwnerUserID != ownerUserID || session.State.Terminal() {
		return domain.ErrNotFound
	}
	session.State = state
	m.sessions[sessionID] = session
	return nil
}

func (m *memoryStore) CloseSession(_ context.Context, ownerUserID, sessionID string, state domain.State, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[sessionID]
	if !ok || session.OwnerUserID != ownerUserID {
		return domain.ErrNotFound
	}
	if session.State.Terminal() {
		return nil
	}
	session.State = state
	session.UpdatedAt = now
	session.ExpiresAt = now
	m.sessions[sessionID] = session
	return nil
}

func (m *memoryStore) ExpireIdle(_ context.Context, now time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	expired := []string{}
	for id, session := range m.sessions {
		if !session.State.Terminal() && session.ExpiresAt.Before(now) {
			session.State = domain.StateClosed
			m.sessions[id] = session
			expired = append(expired, id)
		}
	}
	return expired, nil
}

func (m *memoryStore) CountActive(_ context.Context, ownerUserID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, session := range m.sessions {
		if session.OwnerUserID == ownerUserID && !session.State.Terminal() {
			count++
		}
	}
	return count, nil
}

type fakeDisplay struct {
	mu       sync.Mutex
	exited   bool
	answers  []string
	stopped  bool
	detached bool
}

func (f *fakeDisplay) Connect(_ context.Context, offer string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exited {
		return "", domain.ErrEngineUnavailable
	}
	if len(offer) == 0 || len(offer) > domain.MaxSDPBytes {
		return "", domain.ErrInvalid
	}
	return "v=0\r\nanswer", nil
}

func (f *fakeDisplay) Detach() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detached = true
}

func (f *fakeDisplay) Exited() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exited
}

func (f *fakeDisplay) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
}

type fakeEngine struct {
	mu       sync.Mutex
	count    int
	failNext bool
	displays []*fakeDisplay
}

func (f *fakeEngine) Facts() ports.EngineFacts {
	return ports.EngineFacts{Engine: "fake", ProcessGroupKill: true, ParentDeathSig: true}
}

func (f *fakeEngine) Available(_ context.Context) error { return nil }

func (f *fakeEngine) Reserve() (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.count >= 4 {
		return nil, domain.ErrSessionLimit
	}
	f.count++
	var released bool
	return func() {
		if released {
			return
		}
		released = true
		f.mu.Lock()
		f.count--
		f.mu.Unlock()
	}, nil
}

func (f *fakeEngine) Launch(_ context.Context, width, height int32) (ports.Display, error) {
	f.mu.Lock()
	if f.failNext {
		f.mu.Unlock()
		return nil, errors.New("engine cannot start")
	}
	f.mu.Unlock()
	display := &fakeDisplay{}
	f.mu.Lock()
	f.displays = append(f.displays, display)
	f.mu.Unlock()
	return display, nil
}

type seqGenerator struct{ counter int }

func (s *seqGenerator) New() string {
	s.counter++
	// Deterministic canonical UUIDv7-shaped ids for the memory store tests.
	return fmt.Sprintf("01999999-9999-7999-8999-%07d%03d0f", s.counter/1000, s.counter%1000)
}

const (
	testOwner   = "01999999-9999-7999-8999-000000000001"
	testProject = "01999999-9999-7999-8999-000000000002"
)

func newTestService(t *testing.T) (*Service, *fakeEngine) {
	t.Helper()
	engine := &fakeEngine{}
	service, err := NewService(newMemoryStore(), engine, &seqGenerator{}, slog.Default())
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	return service, engine
}

func TestNativeServiceCreateIdempotency(t *testing.T) {
	service, _ := newTestService(t)
	ctx := context.Background()
	first, err := service.Create(ctx, testOwner, testProject, "key-1", 800, 600)
	if err != nil || first.State != domain.StateRunning {
		t.Fatalf("create: %v %+v", err, first)
	}
	replay, err := service.Create(ctx, testOwner, testProject, "key-1", 800, 600)
	if err != nil || replay.SessionID != first.SessionID {
		t.Fatalf("replay drifted: %v %+v", err, replay)
	}
	if _, err := service.Create(ctx, testOwner, testProject, "key-1", 1024, 768); !errors.Is(err, domain.ErrIdempotencyDrift) {
		t.Fatalf("drift must abort: %v", err)
	}
}

func TestNativeServiceValidationAndCap(t *testing.T) {
	service, _ := newTestService(t)
	ctx := context.Background()
	if _, err := service.Create(ctx, testOwner, testProject, "bad-size", 10, 10); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("size bound: %v", err)
	}
	if _, err := service.Create(ctx, "not-a-uuid", testProject, "bad-owner", 800, 600); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("owner grammar: %v", err)
	}
	// The per-owner cap is two live sessions.
	first, err := service.Create(ctx, testOwner, testProject, "cap-1", 800, 600)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	defer func() { _, _ = service.Close(ctx, testOwner, first.SessionID) }()
	second, err := service.Create(ctx, testOwner, testProject, "cap-2", 800, 600)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	defer func() { _, _ = service.Close(ctx, testOwner, second.SessionID) }()
	if _, err := service.Create(ctx, testOwner, testProject, "cap-3", 800, 600); !errors.Is(err, domain.ErrSessionLimit) {
		t.Fatalf("cap must hold: %v", err)
	}
}

func TestNativeServiceConnectAndClose(t *testing.T) {
	service, _ := newTestService(t)
	ctx := context.Background()
	session, err := service.Create(ctx, testOwner, testProject, "connect", 800, 600)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := service.Connect(ctx, testOwner, session.SessionID, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty offer: %v", err)
	}
	_, answer, err := service.Connect(ctx, testOwner, session.SessionID, "v=0\r\noffer")
	if err != nil || answer == "" {
		t.Fatalf("connect: %v %q", err, answer)
	}
	// Foreign owners never connect.
	if _, _, err := service.Connect(ctx, "01999999-9999-7999-8999-000000000c99", session.SessionID, "v=0\r\noffer"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign connect: %v", err)
	}
	closed, err := service.Close(ctx, testOwner, session.SessionID)
	if err != nil || closed.State != domain.StateClosed {
		t.Fatalf("close: %v %+v", err, closed)
	}
	if _, _, err := service.Connect(ctx, testOwner, session.SessionID, "v=0\r\noffer"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("connect after close: %v", err)
	}
}

func TestNativeServiceDetachKeepsSessionRunning(t *testing.T) {
	service, engine := newTestService(t)
	ctx := context.Background()
	session, err := service.Create(ctx, testOwner, testProject, "detach", 800, 600)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := service.Connect(ctx, testOwner, session.SessionID, "v=0\r\noffer"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	detached, err := service.Detach(ctx, testOwner, session.SessionID)
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if detached.State != domain.StateRunning {
		t.Fatalf("detach changed session state: %s", detached.State)
	}
	engine.mu.Lock()
	display := engine.displays[len(engine.displays)-1]
	engine.mu.Unlock()
	if display == nil || display.stopped {
		t.Fatal("detach stopped the display")
	}
	if !display.detached {
		t.Fatal("detach did not release the media peer")
	}
	if _, _, err := service.Connect(ctx, testOwner, session.SessionID, "v=0\r\noffer"); err != nil {
		t.Fatalf("reconnect after detach: %v", err)
	}
	// Detaching a closed session fails closed.
	if _, err := service.Close(ctx, testOwner, session.SessionID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := service.Detach(ctx, testOwner, session.SessionID); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("detach after close: %v", err)
	}
}

func TestNativeServiceDeadDisplayFailsClosed(t *testing.T) {
	service, engine := newTestService(t)
	ctx := context.Background()
	session, err := service.Create(ctx, testOwner, testProject, "dead", 800, 600)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	engine.mu.Lock()
	display := engine.displays[len(engine.displays)-1]
	engine.mu.Unlock()
	display.mu.Lock()
	display.exited = true
	display.mu.Unlock()
	if _, _, err := service.Connect(ctx, testOwner, session.SessionID, "v=0\r\noffer"); !errors.Is(err, domain.ErrEngineUnavailable) {
		t.Fatalf("dead display must be unavailable: %v", err)
	}
	stored, err := service.Get(ctx, testOwner, session.SessionID)
	if err != nil || stored.State != domain.StateFailed {
		t.Fatalf("dead display must terminal-fail the session: %v %+v", err, stored)
	}
}

func TestNativeServiceLaunchFailureIsUnavailable(t *testing.T) {
	service, engine := newTestService(t)
	engine.mu.Lock()
	engine.failNext = true
	engine.mu.Unlock()
	if _, err := service.Create(context.Background(), testOwner, testProject, "boom", 800, 600); !errors.Is(err, domain.ErrEngineUnavailable) {
		t.Fatalf("launch failure must surface unavailable: %v", err)
	}
}

func (m *memoryStore) ListActive(_ context.Context) ([]domain.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []domain.Session
	for _, session := range m.sessions {
		if !session.State.Terminal() {
			result = append(result, session)
		}
	}
	return result, nil
}

func TestNativeRestartAndExpiryReclaimCapacity(t *testing.T) {
	ctx := context.Background()
	service, engine := newTestService(t)
	first, err := service.Create(ctx, testOwner, testProject, "restart", 800, 600)
	if err != nil {
		t.Fatal(err)
	}
	service.Shutdown()
	restarted, err := NewService(service.store, engine, &seqGenerator{counter: 100}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	replay, err := restarted.Create(ctx, testOwner, testProject, "restart", 800, 600)
	if err != nil || replay.SessionID != first.SessionID || replay.State != domain.StateFailed {
		t.Fatalf("restart revived stale session: %+v %v", replay, err)
	}
	second, err := restarted.Create(ctx, testOwner, testProject, "expiry", 800, 600)
	if err != nil {
		t.Fatal(err)
	}
	store := service.store.(*memoryStore)
	store.mu.Lock()
	row := store.sessions[second.SessionID]
	row.ExpiresAt = time.Now().Add(-time.Second)
	store.sessions[second.SessionID] = row
	store.mu.Unlock()
	if _, _, err := restarted.Connect(ctx, testOwner, second.SessionID, "offer"); err == nil {
		t.Fatal("expired session connected before sweep")
	}
	if engine.count != 0 {
		t.Fatalf("capacity leaked: %d", engine.count)
	}
	closed, err := restarted.Close(ctx, testOwner, first.SessionID)
	if err != nil || closed.State != domain.StateFailed {
		t.Fatal("close replay disagreed with durable terminal state")
	}
}
