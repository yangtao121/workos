package application

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
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
	// internalWinner simulates the repository's in-transaction arbitration:
	// the application-level lookup misses, but Create returns a pre-seeded
	// winning session instead of persisting the caller's fresh one.
	internalWinner *domain.SurfaceSession
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
	if r.internalWinner != nil {
		// The arbitration winner is invisible to the application-level
		// lookup, exactly like a concurrent insert that commits between the
		// lookup and the mapping insert.
		return ports.StoredSessionRequest{}, false, nil
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
	if r.internalWinner != nil {
		winner := *r.internalWinner
		r.sessions[winner.ID] = winner
		r.requests[key] = ports.StoredSessionRequest{RequestDigest: command.RequestDigest, SessionID: winner.ID}
		return winner, nil
	}
	session := command.Session
	session.BridgeTokenHash = command.BridgeTokenHash
	r.sessions[session.ID] = session
	r.requests[key] = ports.StoredSessionRequest{RequestDigest: command.RequestDigest, SessionID: session.ID}
	return session, nil
}

func (r *fakeRepository) RotateBridgeToken(_ context.Context, command ports.RotateBridgeTokenCommand) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return domain.SurfaceSession{}, r.failWith
	}
	session, ok := r.sessions[command.SessionID]
	if !ok || session.OwnerUserID != command.OwnerUserID || session.DeviceID != command.DeviceID {
		return domain.SurfaceSession{}, domain.ErrNotFound
	}
	if session.ClosedAt != nil || !session.ExpiresAt.After(command.Now) {
		return domain.SurfaceSession{}, domain.ErrNotFound
	}
	session.BridgeTokenHash = command.TokenHash
	r.sessions[command.SessionID] = session
	// The store returns the row as of this rotation, atomically: the
	// snapshot never mixes another rotation's hash with this credential.
	return session, nil
}

func (r *fakeRepository) GetActiveSessionByBridgeToken(_ context.Context, ownerUserID, tokenHash string, now time.Time) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return domain.SurfaceSession{}, r.failWith
	}
	for _, session := range r.sessions {
		if session.OwnerUserID != ownerUserID || session.BridgeTokenHash != tokenHash {
			continue
		}
		if session.ClosedAt != nil || !session.ExpiresAt.After(now) {
			continue
		}
		return session, nil
	}
	return domain.SurfaceSession{}, domain.ErrNotFound
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
	resolved   ports.ResolvedLaunch
	assets     map[string]ports.Asset
	resolveErr error
	assetErr   error
	calls      atomic.Int64
}

type fakeWorkloads struct {
	ensureHandle  ports.WorkloadHandle
	lookupHandle  ports.WorkloadHandle
	ensureErr     error
	lookupErr     error
	ensureCalls   int
	lookupCalls   int
	lastEnsure    ports.SurfaceWorkloadQuery
	lastLookupID  string
	lastLookupGen int64
}

func (f *fakeWorkloads) EnsureSurfaceWorkload(_ context.Context, query ports.SurfaceWorkloadQuery) (ports.WorkloadHandle, error) {
	f.ensureCalls++
	f.lastEnsure = query
	if f.ensureErr != nil {
		return ports.WorkloadHandle{}, f.ensureErr
	}
	return f.ensureHandle, nil
}

func (f *fakeWorkloads) LookupSurfaceWorkload(_ context.Context, workloadID string, generation int64) (ports.WorkloadHandle, error) {
	f.lookupCalls++
	f.lastLookupID = workloadID
	f.lastLookupGen = generation
	if f.lookupErr != nil {
		return ports.WorkloadHandle{}, f.lookupErr
	}
	return f.lookupHandle, nil
}

func (f *fakeResolver) ResolveWebBundle(context.Context, ports.ResolveQuery) (ports.LaunchDescriptor, error) {
	f.calls.Add(1)
	if f.resolveErr != nil {
		return ports.LaunchDescriptor{}, f.resolveErr
	}
	return f.descriptor, nil
}

func (f *fakeResolver) ResolveSurfaceLaunch(_ context.Context, _ ports.ResolveQuery) (ports.ResolvedLaunch, error) {
	f.calls.Add(1)
	if f.resolveErr != nil {
		return ports.ResolvedLaunch{}, f.resolveErr
	}
	if f.resolved.Kind == "" {
		resolved := ports.ResolvedLaunch{
			Kind:  ports.LaunchKindWebBundle,
			AppID: f.descriptor.AppID, Version: f.descriptor.Version,
			ManifestDigest: f.descriptor.ManifestDigest,
			ArtifactID:     f.descriptor.ArtifactID, ArtifactDigest: f.descriptor.ArtifactDigest,
			Entrypoint:         f.descriptor.Entrypoint,
			GrantedPermissions: f.descriptor.GrantedPermissions,
			GrantRevision:      f.descriptor.GrantRevision,
		}
		return resolved, nil
	}
	return f.resolved, nil
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

type staticGenerator struct{ counter atomic.Int64 }

func (g *staticGenerator) New() string {
	return fmt.Sprintf("0198d7ea-2110-7c42-b659-%010dab", g.counter.Add(1))
}

func newTestService(repository ports.SessionRepository, resolver ports.LaunchResolver) *Service {
	service, err := New(repository, resolver, &staticGenerator{}, 15*time.Minute)
	if err != nil {
		panic(err)
	}
	return service
}

func newTestServiceWithWorkloads(repository ports.SessionRepository, resolver ports.LaunchResolver, workloads ports.WorkloadRuntime) *Service {
	service, err := NewWithWorkloads(repository, resolver, workloads, &staticGenerator{}, 15*time.Minute)
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
		// A real post-backfill epoch — neither the migration backfill 1 nor
		// the zero value — so persistence tests prove the resolver's value
		// flows through, not a constant.
		GrantRevision: 4,
	}
}

func containerResolution() ports.ResolvedLaunch {
	return ports.ResolvedLaunch{
		Kind:  ports.LaunchKindWebServiceContainer,
		AppID: "notes-app", Version: "1.0.0",
		ManifestDigest: "sha256:" + strings.Repeat("a", 64),
		GrantRevision:  4,
		Image:          "localhost/workos-fixture@sha256:" + strings.Repeat("b", 64),
		Command:        []string{"/workos-fixture", "serve"}, Port: 8080,
		Resources: ports.ContainerPolicy{CPUHardCores: 1, MemoryHighMB: 64, MemoryMaxMB: 96, PidsMax: 32},
		Health:    ports.HealthPolicy{HTTPPath: "/health", StartupSeconds: 10, RestartLimit: 2},
		Route:     "/",
	}
}

func TestCreatePersistsOwnerDeviceBoundSession(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	created, err := service.Create(context.Background(), validCommand("key-1"))
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	session := created.Session
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

func TestCreateAndServeWebServiceUsesExactWorkloadGeneration(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	resolved := containerResolution()
	resolver := &fakeResolver{resolved: resolved}
	workloads := &fakeWorkloads{
		ensureHandle: ports.WorkloadHandle{
			ID: "0198d7ea-2110-7c42-b659-c5e4d73bc390", Generation: 3,
			Endpoint: "127.0.0.1:41000",
		},
		lookupHandle: ports.WorkloadHandle{
			ID: "0198d7ea-2110-7c42-b659-c5e4d73bc390", Generation: 3,
			Endpoint: "127.0.0.1:41000",
		},
	}
	service := newTestServiceWithWorkloads(repository, resolver, workloads)
	command := validCommand("web-service-key")
	command.PreferredRenderer = domain.RendererWebService

	created, err := service.Create(context.Background(), command)
	if err != nil {
		t.Fatalf("create web service: %v", err)
	}
	if created.Session.Renderer != domain.RendererWebService ||
		created.Session.WorkloadID != workloads.ensureHandle.ID ||
		created.Session.WorkloadGeneration != workloads.ensureHandle.Generation {
		t.Fatalf("web-service session lost workload identity: %+v", created.Session)
	}
	if workloads.ensureCalls != 1 || workloads.lastEnsure.OperationKey != "surface-create:web-service-key" ||
		workloads.lastEnsure.ManifestDigest != resolved.ManifestDigest ||
		len(workloads.lastEnsure.Command) != len(resolved.Command) {
		t.Fatalf("workload ensure did not receive the exact resolved descriptor: %+v", workloads.lastEnsure)
	}

	content, err := service.ServeSurface(context.Background(), testOwner, testDevice, created.Session.ID, "assets/app.js")
	if err != nil {
		t.Fatalf("serve web service: %v", err)
	}
	if content.Kind != ports.ContentProxy || content.Proxy.Endpoint != "127.0.0.1:41000" ||
		content.Proxy.BackendPath != "/assets/app.js" || content.Proxy.SessionID != created.Session.ID {
		t.Fatalf("unexpected proxy target: %+v", content)
	}
	if workloads.lookupCalls != 1 || workloads.lastLookupID != workloads.ensureHandle.ID ||
		workloads.lastLookupGen != workloads.ensureHandle.Generation {
		t.Fatalf("proxy lookup did not bind the persisted workload generation: id=%s generation=%d",
			workloads.lastLookupID, workloads.lastLookupGen)
	}
}

func TestWebServiceProxyFailsClosedOnCoreDescriptorDrift(t *testing.T) {
	t.Parallel()
	base := containerResolution()
	mutations := map[string]func(*ports.ResolvedLaunch){
		"renderer": func(value *ports.ResolvedLaunch) { value.Kind = ports.LaunchKindWebBundle },
		"app":      func(value *ports.ResolvedLaunch) { value.AppID = "other-app" },
		"version":  func(value *ports.ResolvedLaunch) { value.Version = "1.0.1" },
		"digest":   func(value *ports.ResolvedLaunch) { value.ManifestDigest = "sha256:" + strings.Repeat("c", 64) },
	}
	for name, mutate := range mutations {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repository := newFakeRepository()
			resolver := &fakeResolver{resolved: base}
			workloads := &fakeWorkloads{
				ensureHandle: ports.WorkloadHandle{ID: "0198d7ea-2110-7c42-b659-c5e4d73bc390", Generation: 1, Endpoint: "127.0.0.1:41000"},
				lookupHandle: ports.WorkloadHandle{ID: "0198d7ea-2110-7c42-b659-c5e4d73bc390", Generation: 1, Endpoint: "127.0.0.1:41000"},
			}
			service := newTestServiceWithWorkloads(repository, resolver, workloads)
			command := validCommand("proxy-drift-" + name)
			command.PreferredRenderer = domain.RendererWebService
			created, err := service.Create(context.Background(), command)
			if err != nil {
				t.Fatalf("seed web-service session: %v", err)
			}
			drifted := base
			mutate(&drifted)
			resolver.resolved = drifted
			_, err = service.ServeSurface(context.Background(), testOwner, testDevice, created.Session.ID, "")
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("descriptor drift verdict %v, want not found", err)
			}
			if workloads.lookupCalls != 0 {
				t.Fatalf("descriptor drift reached workload lookup %d times", workloads.lookupCalls)
			}
		})
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
	if err != nil || replayed.Session.ID != first.Session.ID {
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

// grantEpochDescriptor is one Core resolution carrying an explicit grant set
// and epoch, the shape SetAppGrants makes authoritative.
func grantEpochDescriptor(grant []string, revision int64) ports.LaunchDescriptor {
	descriptor := launchDescriptor()
	descriptor.GrantedPermissions = grant
	descriptor.GrantRevision = revision
	return descriptor
}

// TestCreatePersistsResolverGrantRevision pins the epoch data flow: whatever
// Core's resolver returns — a real post-mutation epoch like 7, not the
// backfill 1 and not the zero value — is exactly what the session persists.
func TestCreatePersistsResolverGrantRevision(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	resolver := &fakeResolver{descriptor: grantEpochDescriptor([]string{domain.BridgeCapabilityAgentTaskRun}, 7)}
	service := newTestService(repository, resolver)
	created, err := service.Create(context.Background(), validCommand("epoch-key"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Session.InstallationGrantRevision != 7 {
		t.Fatalf("session epoch = %d, want resolver epoch 7", created.Session.InstallationGrantRevision)
	}
	repository.mu.Lock()
	stored := repository.sessions[created.Session.ID]
	repository.mu.Unlock()
	if stored.InstallationGrantRevision != 7 {
		t.Fatalf("persisted epoch = %d, want 7", stored.InstallationGrantRevision)
	}
	if stored.BridgeCapabilities == nil || len(stored.BridgeCapabilities) != 1 || stored.BridgeCapabilities[0] != domain.BridgeCapabilityAgentTaskRun {
		t.Fatalf("effective capabilities = %v, want the granted∩implemented snapshot", stored.BridgeCapabilities)
	}
}

// TestCreateFailsClosedOnUntrustworthyGrantRevision pins the trust invariant:
// a resolution whose epoch is below 1 is corruption, not a usable resolution.
// The application refuses it with the sanitized ErrResolverCorrupt verdict,
// persists nothing, and leaves the create key unconsumed — epoch 0 can never
// reach storage (the DB CHECK would also reject it; the application answers
// first with a clean fixed error).
func TestCreateFailsClosedOnUntrustworthyGrantRevision(t *testing.T) {
	t.Parallel()
	for _, revision := range []int64{0, -3} {
		repository := newFakeRepository()
		resolver := &fakeResolver{descriptor: grantEpochDescriptor(nil, revision)}
		service := newTestService(repository, resolver)
		_, err := service.Create(context.Background(), validCommand("corrupt-key"))
		if !errors.Is(err, ports.ErrResolverCorrupt) {
			t.Fatalf("revision %d verdict %v, want ErrResolverCorrupt", revision, err)
		}
		if len(repository.sessions) != 0 || len(repository.requests) != 0 {
			t.Fatalf("revision %d persisted a session or consumed the key", revision)
		}
	}
}

// TestCreateReplayComparesGrantEpoch drives the ADR-0003 §3 replay matrix:
// a matching epoch keeps the existing replay behavior (exact session plus a
// recorded token rotation); a changed epoch fails closed with
// ErrGrantEpochStale before any rotation, so no usable credential bound to
// the superseded epoch is ever minted, the stored snapshot (epoch and
// effective capabilities) is untouched, and only a fresh create key reopens
// under the new grant.
func TestCreateReplayComparesGrantEpoch(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	resolver := &fakeResolver{descriptor: grantEpochDescriptor([]string{domain.BridgeCapabilityAgentTaskRun}, 4)}
	service := newTestService(repository, resolver)
	ctx := context.Background()

	first, err := service.Create(ctx, validCommand("epoch-key"))
	if err != nil {
		t.Fatal(err)
	}
	firstHash := domain.HashBridgeToken(first.BridgeToken)

	// Same epoch: the existing replay contract — same session, rotated token.
	replayed, err := service.Create(ctx, validCommand("epoch-key"))
	if err != nil || replayed.Session.ID != first.Session.ID {
		t.Fatalf("same-epoch replay failed: %v", err)
	}
	if replayed.BridgeToken == "" || domain.HashBridgeToken(replayed.BridgeToken) == firstHash {
		t.Fatal("same-epoch replay did not rotate a fresh credential")
	}
	repository.mu.Lock()
	storedHashAfterReplay := repository.sessions[first.Session.ID].BridgeTokenHash
	repository.mu.Unlock()
	if storedHashAfterReplay != domain.HashBridgeToken(replayed.BridgeToken) {
		t.Fatal("replay rotation was not persisted")
	}

	// The grant mutates (SetAppGrants): epoch 5, watch capability added.
	resolver.descriptor = grantEpochDescriptor(
		[]string{domain.BridgeCapabilityAgentTaskRun, domain.BridgeCapabilityAgentEventWatch}, 5)

	_, err = service.Create(ctx, validCommand("epoch-key"))
	if !errors.Is(err, domain.ErrGrantEpochStale) {
		t.Fatalf("changed-epoch replay verdict %v, want ErrGrantEpochStale", err)
	}
	if strings.Contains(err.Error(), "4") || strings.Contains(err.Error(), "5") {
		t.Fatalf("stale-epoch verdict leaks a revision value: %v", err)
	}
	repository.mu.Lock()
	stored := repository.sessions[first.Session.ID]
	repository.mu.Unlock()
	// No rotation: the last credential is still the same-epoch replay's.
	if stored.BridgeTokenHash != storedHashAfterReplay {
		t.Fatal("changed-epoch replay rotated a credential")
	}
	// The persisted snapshot is untouched — no local grant diff, no epoch bump.
	if stored.InstallationGrantRevision != 4 {
		t.Fatalf("changed-epoch replay rewrote the session epoch to %d", stored.InstallationGrantRevision)
	}
	if len(stored.BridgeCapabilities) != 1 || stored.BridgeCapabilities[0] != domain.BridgeCapabilityAgentTaskRun {
		t.Fatalf("changed-epoch replay mutated the capability snapshot: %v", stored.BridgeCapabilities)
	}

	// Only a fresh create key reopens under the new grant: new session, new
	// epoch, and capabilities recomputed from the new grant ∩ implemented
	// methods.
	reopened, err := service.Create(ctx, validCommand("epoch-key-2"))
	if err != nil {
		t.Fatalf("fresh key after grant change failed: %v", err)
	}
	if reopened.Session.ID == first.Session.ID {
		t.Fatal("fresh key replayed the old session")
	}
	if reopened.Session.InstallationGrantRevision != 5 {
		t.Fatalf("reopened epoch = %d, want 5", reopened.Session.InstallationGrantRevision)
	}
	if len(reopened.Session.BridgeCapabilities) != 2 {
		t.Fatalf("reopened capabilities = %v, want both granted methods", reopened.Session.BridgeCapabilities)
	}
}

// TestClosedReplayWithChangedEpochStillFailsClosed pins the interaction with
// the existing "closed sessions never regain a credential" rule: a changed
// epoch fails closed with the stale verdict regardless of session state, and
// a closed session with a matching epoch still returns its snapshot without
// minting anything.
func TestClosedReplayWithChangedEpochStillFailsClosed(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	resolver := &fakeResolver{descriptor: grantEpochDescriptor(nil, 2)}
	service := newTestService(repository, resolver)
	ctx := context.Background()
	created, err := service.Create(ctx, validCommand("closed-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Close(ctx, testOwner, testDevice, created.Session.ID); err != nil {
		t.Fatal(err)
	}
	// Matching epoch on a closed session: the existing exact, token-free
	// replay.
	replayed, err := service.Create(ctx, validCommand("closed-key"))
	if err != nil || replayed.Session.ID != created.Session.ID || replayed.BridgeToken != "" {
		t.Fatalf("closed same-epoch replay changed shape: %v %+v", err, replayed)
	}
	// Changed epoch: fail closed even for the closed session.
	resolver.descriptor = grantEpochDescriptor(nil, 3)
	if _, err := service.Create(ctx, validCommand("closed-key")); !errors.Is(err, domain.ErrGrantEpochStale) {
		t.Fatalf("closed changed-epoch replay verdict %v, want ErrGrantEpochStale", err)
	}
}

// TestConcurrentArbitrationLoserFailsClosedOnEpochMismatch pins the
// concurrent-replay form of the rule: when the repository's in-transaction
// arbitration returns a winning session pinned to a different grant epoch
// than this request resolved, the loser must not rotate a credential onto
// that session — it fails closed exactly like a sequential replay.
func TestConcurrentArbitrationLoserFailsClosedOnEpochMismatch(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	resolver := &fakeResolver{descriptor: grantEpochDescriptor(nil, 6)}
	service := newTestService(repository, resolver)

	winnerToken, err := domain.NewBridgeToken()
	if err != nil {
		t.Fatal(err)
	}
	winner := domain.SurfaceSession{
		ID: "0198d7ea-2110-7c42-b659-c5e4d73bc381", OwnerUserID: testOwner, DeviceID: testDevice,
		IdempotencyKey: "key-1", RequestDigest: domain.CreateRequestDigest(testDevice, "0198d7ea-2110-7c42-b659-c5e4d73bc341",
			"0198d7ea-2110-7c42-b659-c5e4d73bc342", "desktop", 1024, 768, 2, domain.RendererWebBundle),
		ProjectID: "0198d7ea-2110-7c42-b659-c5e4d73bc341", AppInstanceID: "0198d7ea-2110-7c42-b659-c5e4d73bc342",
		Renderer:                  domain.RendererWebBundle,
		BridgeTokenHash:           domain.HashBridgeToken(winnerToken),
		BridgeCapabilities:        []string{},
		InstallationGrantRevision: 5, // the winner opened under an older epoch
		CreatedAt:                 time.Now().UTC().Add(-time.Minute), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	winner.Path = domain.SessionPath(winner.ID)
	repository.mu.Lock()
	repository.sessions[winner.ID] = winner
	repository.mu.Unlock()
	repository.internalWinner = &winner

	if _, err := service.Create(context.Background(), validCommand("key-1")); !errors.Is(err, domain.ErrGrantEpochStale) {
		t.Fatalf("loser verdict %v, want ErrGrantEpochStale", err)
	}
	repository.mu.Lock()
	storedHash := repository.sessions[winner.ID].BridgeTokenHash
	repository.mu.Unlock()
	if storedHash != domain.HashBridgeToken(winnerToken) {
		t.Fatal("loser rotated a credential onto the superseded-epoch winner")
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
		func() CreateCommand { c := validCommand("k"); c.PreferredRenderer = "native"; return c }(),
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
	if resolver.calls.Load() != 0 {
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
	if err != nil || replayed.Session.ID != first.Session.ID {
		t.Fatalf("same-device replay broken by device binding: %v", err)
	}
	// Even closed, the replay is exact — and the other device stays aborted.
	if _, err := service.Close(ctx, testOwner, testDevice, first.Session.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err = service.Create(ctx, validCommand("key-1"))
	if err != nil || replayed.Session.ID != first.Session.ID || replayed.Session.ClosedAt == nil {
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
	if err != nil || second.Session.ID == first.Session.ID {
		t.Fatalf("independent key on another device failed: %v", err)
	}
}

func TestCloseIsIdempotentAndScoped(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	ctx := context.Background()
	created, err := service.Create(ctx, validCommand("key-1"))
	if err != nil {
		t.Fatal(err)
	}
	session := created.Session
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
	if err != nil || replayed.Session.ID != session.ID || replayed.Session.ClosedAt == nil {
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
	created, err := service.Create(ctx, validCommand("key-1"))
	if err != nil {
		t.Fatal(err)
	}
	session := created.Session
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
	session2Created, err := service.Create(ctx, validCommand("key-2"))
	if err != nil {
		t.Fatal(err)
	}
	session2 := session2Created.Session
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
	created, err := service.Create(ctx, validCommand("key-1"))
	if err != nil {
		t.Fatal(err)
	}
	session := created.Session
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
			created, err := service.Create(context.Background(), command)
			if err != nil {
				failures <- err
				return
			}
			results <- created.Session
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
		created, err := service.Create(ctx, validCommand("key-1"))
		if err != nil {
			t.Fatal(err)
		}
		session := created.Session
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

// TestConcurrentArbitrationLoserRotatesToken pins the concurrent-create
// credential contract: when the repository's in-transaction arbitration
// returns another create's winning session, the loser must NOT return its
// locally minted (never-persisted) token. The response credential must be a
// real rotation recorded on the winning session.
func TestConcurrentArbitrationLoserRotatesToken(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	ctx := context.Background()

	// Seed the arbitration winner: an open session whose persisted hash
	// belongs to a different create.
	winnerToken, err := domain.NewBridgeToken()
	if err != nil {
		t.Fatal(err)
	}
	winner := domain.SurfaceSession{
		ID: "0198d7ea-2110-7c42-b659-c5e4d73bc371", OwnerUserID: testOwner, DeviceID: testDevice,
		IdempotencyKey: "key-1", RequestDigest: domain.CreateRequestDigest(testDevice, "0198d7ea-2110-7c42-b659-c5e4d73bc341",
			"0198d7ea-2110-7c42-b659-c5e4d73bc342", "desktop", 1024, 768, 2, domain.RendererWebBundle),
		ProjectID: "0198d7ea-2110-7c42-b659-c5e4d73bc341", AppInstanceID: "0198d7ea-2110-7c42-b659-c5e4d73bc342",
		Renderer: domain.RendererWebBundle,
		Descriptor: domain.LaunchDescriptor{AppID: "notes-app", Version: "1.0.0",
			ManifestDigest: "sha256:" + strings.Repeat("a", 64), ArtifactID: "0198d7ea-2110-7c42-b659-c5e4d73bc343",
			ArtifactDigest: "sha256:" + strings.Repeat("b", 64), Entrypoint: "index.html"},
		BridgeTokenHash:    domain.HashBridgeToken(winnerToken),
		BridgeCapabilities: []string{},
		// The winner resolved the same epoch this request resolves, so the
		// loser's rotation is a same-epoch replay.
		InstallationGrantRevision: launchDescriptor().GrantRevision,
		CreatedAt:                 time.Now().UTC().Add(-time.Minute), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	winner.Path = domain.SessionPath(winner.ID)
	repository.mu.Lock()
	repository.sessions[winner.ID] = winner
	repository.mu.Unlock()
	repository.internalWinner = &winner

	created, err := service.Create(ctx, validCommand("key-1"))
	if err != nil {
		t.Fatalf("loser create failed: %v", err)
	}
	if created.Session.ID != winner.ID {
		t.Fatalf("loser did not return the winning session: %s", created.Session.ID)
	}
	if created.BridgeToken == "" {
		t.Fatal("loser returned no credential for an open session")
	}
	if domain.HashBridgeToken(created.BridgeToken) == domain.HashBridgeToken(winnerToken) {
		t.Fatal("loser reused the winner's token instead of rotating")
	}
	// The returned credential is a real persisted fact of this session.
	repository.mu.Lock()
	storedHash := repository.sessions[winner.ID].BridgeTokenHash
	repository.mu.Unlock()
	if storedHash != domain.HashBridgeToken(created.BridgeToken) {
		t.Fatal("returned token hash is not the persisted session fact")
	}
	// The rotation invalidated the winner's earlier credential — an explicit
	// recorded rotation, never "the token was never stored".
	if storedHash == domain.HashBridgeToken(winnerToken) {
		t.Fatal("winner token was not rotated out")
	}
}

// TestConcurrentSameKeyCreatesReturnPersistedCredentials drives eight
// same-key creates through a barrier and asserts, for every successful
// response, that the returned session snapshot pairs the returned token with
// exactly the hash that token was stored under — the pairing is checked
// per response, not just at the end, because that is what the old
// rotate-then-re-read implementation got wrong under interleaving. It also
// asserts that exactly one session/mapping survives and that the final
// stored hash is one of the returned credentials' hashes (the last
// linearized rotation).
func TestConcurrentSameKeyCreatesReturnPersistedCredentials(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	const rotators = 8
	var group sync.WaitGroup
	start := make(chan struct{})
	results := make(chan CreatedSurface, rotators)
	failures := make(chan error, rotators)
	for range rotators {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			created, err := service.Create(context.Background(), validCommand("race-key"))
			if err != nil {
				failures <- err
				return
			}
			results <- created
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatalf("race surfaced an error: %v", err)
	}
	observedHashes := map[string]struct{}{}
	ids := map[string]struct{}{}
	for created := range results {
		ids[created.Session.ID] = struct{}{}
		if created.BridgeToken == "" {
			t.Fatal("open-session create returned no credential")
		}
		// Per-response pairing: this response's own credential must be the
		// fact its own snapshot carries — never a later rotation's hash.
		if created.Session.BridgeTokenHash != domain.HashBridgeToken(created.BridgeToken) {
			t.Fatalf("response %s paired its credential with a foreign hash", created.Session.ID)
		}
		observedHashes[domain.HashBridgeToken(created.BridgeToken)] = struct{}{}
	}
	if len(observedHashes) != rotators {
		t.Fatalf("expected %d distinct rotations, saw %d", rotators, len(observedHashes))
	}
	if len(ids) != 1 {
		t.Fatalf("same-key race produced %d distinct sessions", len(ids))
	}
	repository.mu.Lock()
	stored := map[string]domain.SurfaceSession{}
	for id, session := range repository.sessions {
		stored[id] = session
	}
	repository.mu.Unlock()
	if len(stored) != 1 {
		t.Fatalf("same-key race left %d session facts", len(stored))
	}
	if !finalHashOwned(stored, observedHashes) {
		t.Fatal("final persisted hash never belonged to any returned credential")
	}
}

// TestConcurrentReplaysPairTokenWithOwnRotation drives eight concurrent
// replays of one already-consumed key, so every call takes the rotation
// path. Each response must pair its own freshly minted token with the hash
// that token was stored under at its own linearization point — the old
// rotate-then-separately-re-read implementation interleaved the two steps
// and returned a token next to a later rotation's hash.
func TestConcurrentReplaysPairTokenWithOwnRotation(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository, &fakeResolver{descriptor: launchDescriptor()})
	seeded, err := service.Create(context.Background(), validCommand("replay-key"))
	if err != nil {
		t.Fatalf("seed create failed: %v", err)
	}
	const rotators = 8
	var group sync.WaitGroup
	start := make(chan struct{})
	results := make(chan CreatedSurface, rotators)
	failures := make(chan error, rotators)
	for range rotators {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			created, err := service.Create(context.Background(), validCommand("replay-key"))
			if err != nil {
				failures <- err
				return
			}
			results <- created
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatalf("replay race surfaced an error: %v", err)
	}
	observedHashes := map[string]struct{}{}
	for range rotators {
		created := <-results
		if created.Session.ID != seeded.Session.ID {
			t.Fatalf("replay produced a foreign session %s", created.Session.ID)
		}
		if created.BridgeToken == "" {
			t.Fatal("open-session replay returned no credential")
		}
		if created.Session.BridgeTokenHash != domain.HashBridgeToken(created.BridgeToken) {
			t.Fatal("replay response paired its credential with a foreign hash")
		}
		observedHashes[domain.HashBridgeToken(created.BridgeToken)] = struct{}{}
	}
	if len(observedHashes) != rotators {
		t.Fatalf("expected %d distinct rotations, saw %d", rotators, len(observedHashes))
	}
	repository.mu.Lock()
	stored := repository.sessions[seeded.Session.ID]
	repository.mu.Unlock()
	if _, ok := observedHashes[stored.BridgeTokenHash]; !ok {
		t.Fatal("final persisted hash never belonged to any returned credential")
	}
}

func finalHashOwned(stored map[string]domain.SurfaceSession, observed map[string]struct{}) bool {
	for _, session := range stored {
		if _, ok := observed[session.BridgeTokenHash]; ok {
			return true
		}
	}
	return false
}

func (r *fakeRepository) HasActiveSurface(_ context.Context, ownerUserID, appInstanceID string, now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return false, r.failWith
	}
	for _, session := range r.sessions {
		if session.OwnerUserID == ownerUserID && session.AppInstanceID == appInstanceID &&
			session.ClosedAt == nil && session.ExpiresAt.After(now) {
			return true, nil
		}
	}
	return false, nil
}
