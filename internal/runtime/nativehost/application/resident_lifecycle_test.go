package application

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

type residentMemoryStore struct {
	*memoryStore
	restarts map[string]int64
	stops    map[string]bool
}

func newResidentMemoryStore() *residentMemoryStore {
	return &residentMemoryStore{memoryStore: newMemoryStore(), restarts: map[string]int64{}, stops: map[string]bool{}}
}

func (m *residentMemoryStore) BindChild(_ context.Context, owner, id string, child ports.ChildIdentity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s.OwnerUserID != owner || s.Generation != child.Generation {
		return domain.ErrStoreUnavailable
	}
	s.ChildContainerID, s.ChildImageID, s.ChildGeneration = child.ContainerID, child.ImageID, child.Generation
	m.sessions[id] = s
	return nil
}
func (m *residentMemoryStore) RestartReceipt(_ context.Context, _, _, key string) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.restarts[key]
	return g, ok, nil
}
func (m *residentMemoryStore) StopReceipt(_ context.Context, _, _, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stops[key], nil
}
func (m *residentMemoryStore) BeginRestart(_ context.Context, owner, id, key string, _ time.Time, modes ...domain.LifecycleMode) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.restarts[key]; ok {
		return g, false, nil
	}
	s := m.sessions[id]
	if s.OwnerUserID != owner {
		return 0, false, domain.ErrNotFound
	}
	s.Generation++
	s.State = domain.StateQueued
	s.LifecycleMode, _ = domain.NormalizeLifecycle(modes...)
	m.sessions[id] = s
	m.restarts[key] = s.Generation
	return s.Generation, true, nil
}
func (m *residentMemoryStore) BeginStop(_ context.Context, owner, id, key string, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stops[key] {
		return false, nil
	}
	s := m.sessions[id]
	if s.OwnerUserID != owner {
		return false, domain.ErrNotFound
	}
	s.State = domain.StateClosed
	m.sessions[id] = s
	m.stops[key] = true
	return true, nil
}

type residentTestDisplay struct {
	fakeDisplay
	barrier   sync.Mutex
	identity  ports.ChildIdentity
	stopErr   error
	stopCalls int
	onEvent   func(*surfacev1.SendGreenfieldWindowInputRequest)
	events    []uint64
}

func (d *residentTestDisplay) ChildIdentity() ports.ChildIdentity { return d.identity }
func (d *residentTestDisplay) PreserveForAdoption()               {}
func (d *residentTestDisplay) AroundInput(_ context.Context, fn func() error) error {
	d.barrier.Lock()
	defer d.barrier.Unlock()
	return fn()
}
func (d *residentTestDisplay) StopExact(context.Context) error {
	d.stopCalls++
	if d.stopErr != nil {
		return d.stopErr
	}
	d.Stop()
	return nil
}
func (d *residentTestDisplay) SubscribeWindows() (<-chan *surfacev1.GreenfieldWindowSnapshot, func(), error) {
	ch := make(chan *surfacev1.GreenfieldWindowSnapshot, 1)
	return ch, func() { close(ch) }, nil
}
func (d *residentTestDisplay) SubscribeFrames(string) (<-chan []*surfacev1.GreenfieldWindowFrameTile, func(), error) {
	ch := make(chan []*surfacev1.GreenfieldWindowFrameTile, 1)
	return ch, func() { close(ch) }, nil
}
func (d *residentTestDisplay) SendWindowEvent(_ context.Context, req *surfacev1.SendGreenfieldWindowInputRequest) (*surfacev1.SendGreenfieldWindowInputResponse, error) {
	seq := req.GetEvents()[0].GetSequence()
	d.events = append(d.events, seq)
	if d.onEvent != nil {
		d.onEvent(req)
	}
	return &surfacev1.SendGreenfieldWindowInputResponse{Verdict: surfacev1.GreenfieldInputVerdict_GREENFIELD_INPUT_VERDICT_APPLIED, LastAppliedSequence: seq}, nil
}
func (d *residentTestDisplay) ReadWindowClipboard(context.Context, *surfacev1.ReadGreenfieldClipboardRequest) (*surfacev1.ReadGreenfieldClipboardResponse, error) {
	return &surfacev1.ReadGreenfieldClipboardResponse{TextUtf8: []byte("native")}, nil
}

type residentTestEngine struct {
	*fakeEngine
	created []*residentTestDisplay
	reaped  int
}

func (e *residentTestEngine) LaunchResident(_ context.Context, spec ports.ResidentLaunch) (ports.Display, error) {
	d := &residentTestDisplay{identity: ports.ChildIdentity{ContainerID: spec.Session.SessionID + ":" + string(rune('0'+spec.Session.Generation)), ImageID: "sha256:test", Generation: spec.Session.Generation}}
	e.created = append(e.created, d)
	return d, nil
}
func (e *residentTestEngine) AdoptResident(context.Context, ports.ResidentLaunch) (ports.Display, error) {
	return nil, domain.ErrResidentChildNotFound
}
func (e *residentTestEngine) ReapResident(context.Context, ports.ResidentLaunch) error {
	e.reaped++
	return nil
}

func TestResidentRestartStopExactReapAndReplay(t *testing.T) {
	ctx := context.Background()
	store := newResidentMemoryStore()
	engine := &residentTestEngine{fakeEngine: &fakeEngine{}}
	service, err := NewService(store, engine, &seqGenerator{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Create(ctx, testOwner, testProject, "resident", 800, 600, domain.LifecycleManualStop)
	if err != nil {
		t.Fatal(err)
	}
	first := engine.created[0]
	first.stopErr = errors.New("docker unavailable")
	if _, err := service.Restart(ctx, testOwner, session.SessionID, "restart-1", nil, domain.LifecycleManualStop); err == nil {
		t.Fatal("restart succeeded despite exact child removal failure")
	}
	current, _ := store.GetSession(ctx, testOwner, session.SessionID)
	if current.Generation != 1 || current.State != domain.StateRunning || len(store.restarts) != 0 {
		t.Fatalf("failed removal advanced generation: %+v", current)
	}
	first.stopErr = nil
	restarted, err := service.Restart(ctx, testOwner, session.SessionID, "restart-1", nil, domain.LifecycleManualStop)
	if err != nil || restarted.Generation != 2 || !first.stopped {
		t.Fatalf("restart did not reap and relaunch: %v %+v", err, restarted)
	}
	second := engine.created[1]
	if _, err := service.Restart(ctx, testOwner, session.SessionID, "restart-1", nil, domain.LifecycleManualStop); err != nil || second.stopCalls != 0 {
		t.Fatalf("restart replay touched new child: %v calls=%d", err, second.stopCalls)
	}
	second.stopErr = errors.New("docker unavailable")
	if _, err := service.Stop(ctx, testOwner, session.SessionID, "stop-1", nil); err == nil {
		t.Fatal("stop succeeded despite exact child removal failure")
	}
	current, _ = store.GetSession(ctx, testOwner, session.SessionID)
	if current.State != domain.StateRunning || store.stops["stop-1"] {
		t.Fatalf("failed stop changed durable state: %+v", current)
	}
	second.stopErr = nil
	stopped, err := service.Stop(ctx, testOwner, session.SessionID, "stop-1", nil)
	if err != nil || stopped.State != domain.StateClosed {
		t.Fatalf("stop: %v %+v", err, stopped)
	}
	third, err := service.Restart(ctx, testOwner, session.SessionID, "restart-2", nil, domain.LifecycleManualStop)
	if err != nil || third.Generation != 3 {
		t.Fatalf("restart after stop: %v %+v", err, third)
	}
	newChild := engine.created[2]
	if _, err := service.Stop(ctx, testOwner, session.SessionID, "stop-1", nil); err != nil || newChild.stopCalls != 0 {
		t.Fatalf("old stop replay touched generation 3: %v calls=%d", err, newChild.stopCalls)
	}
}

var _ ports.ResidentEngine = (*residentTestEngine)(nil)
var _ ports.ResidentDisplay = (*residentTestDisplay)(nil)
var _ ports.WindowDisplay = (*residentTestDisplay)(nil)
