package application

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

const (
	testOwner  = "0198d7ea-2110-7c42-b659-c5e4d73bc337"
	testDevice = "0198d7ea-2110-7c42-b659-c5e4d73bc338"
)

// fakeRepository mirrors the durable session semantics: consumed keys rule
// first, sessions are owner/device bound, close is idempotent, and active
// reads fail closed on closed/expired sessions.
type fakeRepository struct {
	mu       sync.Mutex
	sessions map[string]domain.SurfaceSession
	requests map[string]ports.StoredSessionRequest
	failWith error
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{sessions: map[string]domain.SurfaceSession{}, requests: map[string]ports.StoredSessionRequest{}}
}

func (r *fakeRepository) LookupRequest(_ context.Context, ownerUserID, key string) (ports.StoredSessionRequest, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return ports.StoredSessionRequest{}, false, r.failWith
	}
	stored, ok := r.requests[ownerUserID+"/"+key]
	return stored, ok, nil
}

func (r *fakeRepository) GetSession(_ context.Context, ownerUserID, deviceID, sessionID string) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.find(ownerUserID, deviceID, sessionID)
}

func (r *fakeRepository) GetActiveSession(_ context.Context, ownerUserID, deviceID, sessionID string, now time.Time) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, err := r.find(ownerUserID, deviceID, sessionID)
	if err != nil {
		return domain.SurfaceSession{}, err
	}
	if session.ClosedAt != nil || !session.ExpiresAt.After(now) {
		return domain.SurfaceSession{}, domain.ErrNotFound
	}
	return session, nil
}

func (r *fakeRepository) find(ownerUserID, deviceID, sessionID string) (domain.SurfaceSession, error) {
	if r.failWith != nil {
		return domain.SurfaceSession{}, r.failWith
	}
	session, ok := r.sessions[sessionID]
	if !ok || session.OwnerUserID != ownerUserID || session.DeviceID != deviceID {
		return domain.SurfaceSession{}, domain.ErrNotFound
	}
	return session, nil
}

func (r *fakeRepository) Create(_ context.Context, command ports.CreateSessionCommand) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return domain.SurfaceSession{}, r.failWith
	}
	key := command.Session.OwnerUserID + "/" + command.IdempotencyKey
	if stored, ok := r.requests[key]; ok {
		if stored.RequestDigest != command.RequestDigest {
			return domain.SurfaceSession{}, domain.ErrIdempotencyConflict
		}
		return r.sessions[stored.SessionID], nil
	}
	r.sessions[command.Session.ID] = command.Session
	r.requests[key] = ports.StoredSessionRequest{RequestDigest: command.RequestDigest, SessionID: command.Session.ID}
	return command.Session, nil
}

func (r *fakeRepository) Close(_ context.Context, ownerUserID, deviceID, sessionID string, now time.Time) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return domain.SurfaceSession{}, r.failWith
	}
	session, ok := r.sessions[sessionID]
	if !ok || session.OwnerUserID != ownerUserID || session.DeviceID != deviceID {
		return domain.SurfaceSession{}, domain.ErrNotFound
	}
	if session.ClosedAt == nil {
		closed := now
		session.ClosedAt = &closed
		r.sessions[sessionID] = session
	}
	return session, nil
}

// fakeResolver stands in for the Core client with the port sentinels.
type fakeResolver struct {
	descriptor ports.LaunchDescriptor
	assets     map[string]ports.Asset
	resolveErr error
	assetErr   error
	calls      int
}

func (f *fakeResolver) ResolveWebBundle(context.Context, ports.ResolveQuery) (ports.LaunchDescriptor, error) {
	f.calls++
	if f.resolveErr != nil {
		return ports.LaunchDescriptor{}, f.resolveErr
	}
	return f.descriptor, nil
}

func (f *fakeResolver) ReadWebBundleAsset(_ context.Context, query ports.AssetQuery) (ports.Asset, error) {
	if f.assetErr != nil {
		return ports.Asset{}, f.assetErr
	}
	asset, ok := f.assets[query.AssetPath]
	if !ok {
		return ports.Asset{}, ports.ErrResolverNotFound
	}
	return asset, nil
}

type staticGenerator struct{ counter int }

func (g *staticGenerator) New() string {
	g.counter++
	return fmt.Sprintf("0198d7ea-2110-7c42-b659-%010dab", g.counter)
}

func newTestService(repository ports.SessionRepository, resolver ports.LaunchResolver) *Service {
	service, err := New(repository, resolver, &staticGenerator{}, 15*time.Minute)
	if err != nil {
		panic(err)
	}
	return service
}

func validCommand(key string) CreateCommand {
	return CreateCommand{
		OwnerUserID: testOwner, DeviceID: testDevice, IdempotencyKey: key,
		ProjectID:     "0198d7ea-2110-7c42-b659-c5e4d73bc341",
		AppInstanceID: "0198d7ea-2110-7c42-b659-c5e4d73bc342",
		DeviceClass:   "desktop", ViewportWidth: 1024, ViewportHeight: 768, ViewportRatio: 2,
		PreferredRenderer: domain.RendererWebBundle,
	}
}

func launchDescriptor() ports.LaunchDescriptor {
	return ports.LaunchDescriptor{
		AppID: "notes-app", Version: "1.0.0",
		ManifestDigest: "sha256:" + strings.Repeat("a", 64),
		ArtifactID:     "0198d7ea-2110-7c42-b659-c5e4d73bc343",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64),
		Entrypoint:     "index.html",
	}
}

func TestCreatePersistsOwnerDeviceBoundSession(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	session, err := service.Create(context.Background(), validCommand("key-1"))
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if session.OwnerUserID != testOwner || session.DeviceID != testDevice {
		t.Fatal("session not owner/device bound")
	}
	if session.Renderer != domain.RendererWebBundle || session.Path != "/surfaces/"+session.ID+"/" {
		t.Fatalf("unexpected session shape: %+v", session)
	}
	if session.Descriptor.AppID != "notes-app" || session.Descriptor.Entrypoint != "index.html" {
		t.Fatalf("descriptor snapshot missing: %+v", session.Descriptor)
	}
	if !session.ExpiresAt.After(session.CreatedAt) {
		t.Fatal("expiry missing")
	}
	if session.ClosedAt != nil {
		t.Fatal("fresh session is closed")
	}
}

func TestCreateIdempotencyAndConflicts(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	ctx := context.Background()
	first, err := service.Create(ctx, validCommand("key-1"))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Create(ctx, validCommand("key-1"))
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("same-key replay failed: %v", err)
	}
	mutated := validCommand("key-1")
	mutated.ViewportWidth = 800
	if _, err := service.Create(ctx, mutated); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("different request did not abort: %v", err)
	}
	mutatedProject := validCommand("key-1")
	mutatedProject.ProjectID = "0198d7ea-2110-7c42-b659-c5e4d73bc349"
	if _, err := service.Create(ctx, mutatedProject); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("different project did not abort: %v", err)
	}
}

func TestCreateValidationFailuresDoNotConsumeKey(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	resolver := &fakeResolver{descriptor: launchDescriptor()}
	service := newTestService(repository, resolver)
	ctx := context.Background()
	invalid := []CreateCommand{
		{OwnerUserID: "", DeviceID: testDevice, IdempotencyKey: "k", ProjectID: "0198d7ea-2110-7c42-b659-c5e4d73bc341", AppInstanceID: "0198d7ea-2110-7c42-b659-c5e4d73bc342", DeviceClass: "desktop", ViewportWidth: 10, ViewportHeight: 10},
		func() CreateCommand { c := validCommand("k"); c.IdempotencyKey = ""; return c }(),
		func() CreateCommand { c := validCommand("k"); c.ProjectID = "nope"; return c }(),
		func() CreateCommand { c := validCommand("k"); c.AppInstanceID = "nope"; return c }(),
		func() CreateCommand { c := validCommand("k"); c.DeviceClass = ""; return c }(),
		func() CreateCommand { c := validCommand("k"); c.ViewportWidth = 0; return c }(),
		func() CreateCommand { c := validCommand("k"); c.PreferredRenderer = "web-service"; return c }(),
		func() CreateCommand { c := validCommand("k"); c.ViewportRatio = math.NaN(); return c }(),
		func() CreateCommand { c := validCommand("k"); c.ViewportRatio = math.Inf(1); return c }(),
		func() CreateCommand {
			c := validCommand("k")
			c.ProjectID = "0198d7ea-2110-4c42-b659-c5e4d73bc341"
			return c
		}(),
	}
	for index, command := range invalid {
		if _, err := service.Create(ctx, command); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("invalid command %d accepted: %v", index, err)
		}
	}
	if resolver.calls != 0 {
		t.Fatal("resolver was called before validation passed")
	}
	if _, ok := repository.requests[testOwner+"/k"]; ok {
		t.Fatal("failed validation consumed the key")
	}
	// Resolution failures do not consume the key either.
	resolver.resolveErr = ports.ErrResolverNotFound
	if _, err := service.Create(ctx, validCommand("k")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("resolver denial verdict: %v", err)
	}
	resolver.resolveErr = ports.ErrResolverUnsupported
	if _, err := service.Create(ctx, validCommand("k")); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("unsupported runtime verdict: %v", err)
	}
	resolver.resolveErr = ports.ErrResolverUnavailable
	if _, err := service.Create(ctx, validCommand("k")); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("unavailable verdict: %v", err)
	}
	resolver.resolveErr = nil
	if _, err := service.Create(ctx, validCommand("k")); err != nil {
		t.Fatalf("key was consumed by failures: %v", err)
	}
}

// TestSameKeyFromAnotherTrustedDeviceAborts pins the device-bound idempotency
// contract: one key means one (owner, device, canonical request) combination.
// A second trusted device replaying the key is a stable abort decided by the
// stored digest — never a session-lookup miss — while the first device keeps
// replaying its exact session, closed or not.
func TestSameKeyFromAnotherTrustedDeviceAborts(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	ctx := context.Background()
	first, err := service.Create(ctx, validCommand("key-1"))
	if err != nil {
		t.Fatal(err)
	}
	otherDevice := validCommand("key-1")
	otherDevice.DeviceID = "0198d7ea-2110-7c42-b659-c5e4d73bc344"
	if _, err := service.Create(ctx, otherDevice); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same key from another trusted device did not abort: %v", err)
	}
	// The original device still replays the first session snapshot.
	replayed, err := service.Create(ctx, validCommand("key-1"))
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("same-device replay broken by device binding: %v", err)
	}
	// Even closed, the replay is exact — and the other device stays aborted.
	if _, err := service.Close(ctx, testOwner, testDevice, first.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err = service.Create(ctx, validCommand("key-1"))
	if err != nil || replayed.ID != first.ID || replayed.ClosedAt == nil {
		t.Fatalf("closed-session replay must return the first snapshot: %v %+v", err, replayed)
	}
	if _, err := service.Create(ctx, otherDevice); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("other device abort regressed after close: %v", err)
	}
	// Different keys on different devices are independent creates.
	second, err := service.Create(ctx, func() CreateCommand {
		command := validCommand("key-2")
		command.DeviceID = otherDevice.DeviceID
		return command
	}())
	if err != nil || second.ID == first.ID {
		t.Fatalf("independent key on another device failed: %v", err)
	}
}

func TestCloseIsIdempotentAndScoped(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	ctx := context.Background()
	session, err := service.Create(ctx, validCommand("key-1"))
	if err != nil {
		t.Fatal(err)
	}
	closed, err := service.Close(ctx, testOwner, testDevice, session.ID)
	if err != nil || closed.ClosedAt == nil {
		t.Fatalf("first close failed: %v", err)
	}
	repeated, err := service.Close(ctx, testOwner, testDevice, session.ID)
	if err != nil || repeated.ClosedAt == nil || repeated.ClosedAt != closed.ClosedAt {
		t.Fatalf("repeated close changed the first result: %v", err)
	}
	if _, err := service.Close(ctx, testOwner, "0198d7ea-2110-7c42-b659-c5e4d73bc399", session.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign device close leaked: %v", err)
	}
	if _, err := service.Close(ctx, testOwner, testDevice, "0198d7ea-2110-7c42-b659-c5e4d73bc398"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown session close verdict: %v", err)
	}
	// A replay of the create key still returns the first (closed) snapshot.
	replayed, err := service.Create(ctx, validCommand("key-1"))
	if err != nil || replayed.ID != session.ID || replayed.ClosedAt == nil {
		t.Fatalf("replay after close must not resurrect: %v %+v", err, replayed)
	}
}

func TestServeAssetFailsClosed(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	resolver := &fakeResolver{
		descriptor: launchDescriptor(),
		assets: map[string]ports.Asset{
			"":           {Content: []byte("<p>ok</p>"), MediaType: "text/html; charset=utf-8", Etag: "sha256:" + strings.Repeat("c", 64)},
			"index.html": {Content: []byte("<p>ok</p>"), MediaType: "text/html; charset=utf-8", Etag: "sha256:" + strings.Repeat("c", 64)},
		},
	}
	service := newTestService(repository, resolver)
	ctx := context.Background()
	session, err := service.Create(ctx, validCommand("key-1"))
	if err != nil {
		t.Fatal(err)
	}
	asset, err := service.ServeAsset(ctx, testOwner, testDevice, session.ID, "")
	if err != nil || string(asset.Content) != "<p>ok</p>" {
		t.Fatalf("entrypoint asset failed: %v", err)
	}
	direct, err := service.ServeAsset(ctx, testOwner, testDevice, session.ID, "index.html")
	if err != nil || direct.Etag != asset.Etag {
		t.Fatalf("explicit asset failed: %v", err)
	}
	// Unknown files deny.
	if _, err := service.ServeAsset(ctx, testOwner, testDevice, session.ID, "missing.js"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing asset verdict: %v", err)
	}
	// Foreign owner/device cannot read even with a guessed session id.
	if _, err := service.ServeAsset(ctx, "0198d7ea-2110-7c42-b659-c5e4d73bc397", testDevice, session.ID, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign owner read leaked: %v", err)
	}
	if _, err := service.ServeAsset(ctx, testOwner, "0198d7ea-2110-7c42-b659-c5e4d73bc396", session.ID, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign device read leaked: %v", err)
	}
	// Closed sessions stop serving.
	if _, err := service.Close(ctx, testOwner, testDevice, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ServeAsset(ctx, testOwner, testDevice, session.ID, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("closed session still serving: %v", err)
	}
	// Core unavailability maps to Unavailable for the HTTP 503 path.
	session2, err := service.Create(ctx, validCommand("key-2"))
	if err != nil {
		t.Fatal(err)
	}
	resolver.assetErr = ports.ErrResolverUnavailable
	if _, err := service.ServeAsset(ctx, testOwner, testDevice, session2.ID, ""); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("core unavailability verdict: %v", err)
	}
}

func TestExpiredSessionsFailClosed(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	ctx := context.Background()
	session, err := service.Create(ctx, validCommand("key-1"))
	if err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	expired := session
	expired.ExpiresAt = expired.CreatedAt.Add(-time.Second)
	repository.sessions[session.ID] = expired
	repository.mu.Unlock()
	if _, err := service.ServeAsset(ctx, testOwner, testDevice, session.ID, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired session still serving: %v", err)
	}
}

func TestNewValidatesTTLBounds(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	resolver := &fakeResolver{}
	for _, ttl := range []time.Duration{0, -time.Minute, 30 * time.Second, 25 * time.Hour} {
		if _, err := New(repository, resolver, &staticGenerator{}, ttl); err == nil {
			t.Errorf("ttl %s accepted", ttl)
		}
	}
	for _, ttl := range []time.Duration{time.Minute, 15 * time.Minute, 24 * time.Hour} {
		if _, err := New(repository, resolver, &staticGenerator{}, ttl); err != nil {
			t.Errorf("ttl %s rejected: %v", ttl, err)
		}
	}
}

func TestRepositoryFailureIsNotADomainVerdict(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	repository.failWith = errors.New("pool exhausted")
	_, err := service.Create(context.Background(), validCommand("key-1"))
	if err == nil || errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatal("infrastructure error surfaced as a domain verdict")
	}
}

// TestTransientStoreFailureIsUnavailable pins the transient-vs-invariant
// split at the application boundary: an adapter-classified store outage is
// Unavailable (503 at both transports), never NotFound or Internal.
func TestTransientStoreFailureIsUnavailable(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	repository.failWith = ports.ErrStoreUnavailable
	if _, err := service.Create(context.Background(), validCommand("key-1")); !errors.Is(err, ports.ErrStoreUnavailable) {
		t.Fatalf("store outage lost its sentinel: %v", err)
	}
}

// TestConcurrentSameKeyCreatesOneSessionFact drives two same-key creates
// through a barrier without sleeps and asserts exactly one session fact and
// one mapping survive the race. It runs under -race in CI.
func TestConcurrentSameKeyCreatesOneSessionFact(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	var group sync.WaitGroup
	start := make(chan struct{})
	results := make(chan domain.SurfaceSession, 2)
	failures := make(chan error, 2)
	for _, device := range []string{testDevice, testDevice} {
		group.Add(1)
		go func(device string) {
			defer group.Done()
			<-start
			command := validCommand("race-key")
			command.DeviceID = device
			session, err := service.Create(context.Background(), command)
			if err != nil {
				failures <- err
				return
			}
			results <- session
		}(device)
	}
	close(start)
	group.Wait()
	close(results)
	close(failures)
	ids := map[string]struct{}{}
	for session := range results {
		ids[session.ID] = struct{}{}
	}
	if len(ids) != 1 {
		t.Fatalf("same-key race produced %d distinct sessions: %v (failures %v)", len(ids), ids, collectFailures(failures))
	}
	if len(repository.sessions) != 1 || len(repository.requests) != 1 {
		t.Fatalf("same-key race left %d sessions and %d mappings", len(repository.sessions), len(repository.requests))
	}
}

// TestConcurrentCloseAndAssetServeLinearizesClose races one Close against one
// asset serve through a barrier. Both orders are legitimate; what must hold
// is that after Close returns, serving always fails closed.
func TestConcurrentCloseAndAssetServeLinearizesClose(t *testing.T) {
	t.Parallel()
	for iteration := 0; iteration < 32; iteration++ {
		repository := newFakeRepository()
		resolver := &fakeResolver{descriptor: launchDescriptor(), assets: map[string]ports.Asset{
			"": {Content: []byte("<p>ok</p>"), MediaType: "text/html; charset=utf-8", Etag: "e"},
		}}
		service := newTestService(repository, resolver)
		ctx := context.Background()
		session, err := service.Create(ctx, validCommand("key-1"))
		if err != nil {
			t.Fatal(err)
		}
		var group sync.WaitGroup
		start := make(chan struct{})
		served := make(chan error, 1)
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := service.ServeAsset(ctx, testOwner, testDevice, session.ID, "")
			served <- err
		}()
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := service.Close(ctx, testOwner, testDevice, session.ID)
			if err != nil {
				t.Errorf("close failed: %v", err)
			}
		}()
		close(start)
		group.Wait()
		assetErr := <-served
		if assetErr != nil && !errors.Is(assetErr, domain.ErrNotFound) {
			t.Fatalf("in-flight asset race verdict: %v", assetErr)
		}
		// Whatever the interleaving was, a post-close asset request fails
		// closed.
		if _, err := service.ServeAsset(ctx, testOwner, testDevice, session.ID, ""); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("asset served after close returned: %v", err)
		}
	}
}

func collectFailures(channel chan error) []error {
	result := []error{}
	for err := range channel {
		result = append(result, err)
	}
	return result
}

var _ ports.SessionRepository = (*fakeRepository)(nil)
var _ ports.LaunchResolver = (*fakeResolver)(nil)
