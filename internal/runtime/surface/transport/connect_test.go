package transport

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/application"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

const (
	connectOwner  = "0198d7ea-2110-7c42-b659-c5e4d73bc337"
	connectDevice = "0198d7ea-2110-7c42-b659-c5e4d73bc338"
	otherDevice   = "0198d7ea-2110-7c42-b659-c5e4d73bc344"
)

// handlerRepository mirrors the durable session semantics like the
// application test fake; failure injection classifies the way the postgres
// adapter does.
type handlerRepository struct {
	mu       sync.Mutex
	sessions map[string]domain.SurfaceSession
	requests map[string]ports.StoredSessionRequest
	failWith error
}

func newHandlerRepository() *handlerRepository {
	return &handlerRepository{sessions: map[string]domain.SurfaceSession{}, requests: map[string]ports.StoredSessionRequest{}}
}

func (r *handlerRepository) LookupRequest(_ context.Context, ownerUserID, key string) (ports.StoredSessionRequest, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return ports.StoredSessionRequest{}, false, r.failWith
	}
	stored, ok := r.requests[ownerUserID+"/"+key]
	return stored, ok, nil
}

func (r *handlerRepository) GetSession(_ context.Context, ownerUserID, deviceID, sessionID string) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.find(ownerUserID, deviceID, sessionID)
}

func (r *handlerRepository) GetActiveSession(_ context.Context, ownerUserID, deviceID, sessionID string, now time.Time) (domain.SurfaceSession, error) {
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

func (r *handlerRepository) find(ownerUserID, deviceID, sessionID string) (domain.SurfaceSession, error) {
	if r.failWith != nil {
		return domain.SurfaceSession{}, r.failWith
	}
	session, ok := r.sessions[sessionID]
	if !ok || session.OwnerUserID != ownerUserID || session.DeviceID != deviceID {
		return domain.SurfaceSession{}, domain.ErrNotFound
	}
	return session, nil
}

func (r *handlerRepository) Create(_ context.Context, command ports.CreateSessionCommand) (domain.SurfaceSession, error) {
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
	session := command.Session
	session.BridgeTokenHash = command.BridgeTokenHash
	r.sessions[session.ID] = session
	r.requests[key] = ports.StoredSessionRequest{RequestDigest: command.RequestDigest, SessionID: session.ID}
	return session, nil
}

func (r *handlerRepository) RotateBridgeToken(_ context.Context, command ports.RotateBridgeTokenCommand) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[command.SessionID]
	if !ok || session.OwnerUserID != command.OwnerUserID || session.DeviceID != command.DeviceID {
		return domain.SurfaceSession{}, domain.ErrNotFound
	}
	if session.ClosedAt != nil || !session.ExpiresAt.After(command.Now) {
		return domain.SurfaceSession{}, domain.ErrNotFound
	}
	session.BridgeTokenHash = command.TokenHash
	r.sessions[command.SessionID] = session
	return session, nil
}

func (r *handlerRepository) GetActiveSessionByBridgeToken(_ context.Context, ownerUserID, tokenHash string, now time.Time) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *handlerRepository) Close(_ context.Context, ownerUserID, deviceID, sessionID string, now time.Time) (domain.SurfaceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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

// handlerResolver stands in for the Core client adapter.
type handlerResolver struct {
	descriptor ports.LaunchDescriptor
	calls      int
}

func (f *handlerResolver) ResolveWebBundle(context.Context, ports.ResolveQuery) (ports.LaunchDescriptor, error) {
	f.calls++
	return f.descriptor, nil
}

func (f *handlerResolver) ReadWebBundleAsset(context.Context, ports.AssetQuery) (ports.Asset, error) {
	return ports.Asset{}, ports.ErrResolverNotFound
}

type countingGenerator struct {
	counter int
}

func (g *countingGenerator) New() string {
	g.counter++
	return fmt.Sprintf("0198d7ea-2110-7c42-b659-%08dxxxx", g.counter)
}

// newSurfaceServer wires the real Connect handler over the application
// service with the in-memory fakes, behind the trusted identity middleware.
func newSurfaceServer(t *testing.T) (surfacev1connect.SurfaceServiceClient, *handlerRepository, *handlerResolver) {
	t.Helper()
	repository := newHandlerRepository()
	resolver := &handlerResolver{descriptor: ports.LaunchDescriptor{
		AppID: "notes-app", Version: "1.0.0",
		ManifestDigest: "sha256:" + strings.Repeat("a", 64),
		ArtifactID:     "0198d7ea-2110-7c42-b659-c5e4d73bc343",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64),
		Entrypoint:     "index.html",
		GrantRevision:  2,
	}}
	service, err := application.New(repository, resolver, &countingGenerator{}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, handler := NewConnectHandler(service)
	server := httptest.NewServer(identity.Middleware(handler))
	t.Cleanup(server.Close)
	client := surfacev1connect.NewSurfaceServiceClient(server.Client(), server.URL)
	return client, repository, resolver
}

func createRequest(key string, renderer surfacev1.SurfaceRenderer, ratio float64, device string) *connect.Request[surfacev1.CreateSurfaceRequest] {
	request := connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey:    key,
		ProjectId:         "0198d7ea-2110-7c42-b659-c5e4d73bc341",
		AppInstanceId:     "0198d7ea-2110-7c42-b659-c5e4d73bc342",
		DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:          &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: ratio},
		PreferredRenderer: renderer,
	})
	request.Header().Set(identity.UserHeader, connectOwner)
	request.Header().Set(identity.DeviceHeader, device)
	return request
}

// TestCreateSurfaceFailsClosedOnUnsupportedRenderers drives the real Connect
// handler: every declared-but-unimplemented renderer and an unknown numeric
// enum value must be a stable InvalidArgument that never reaches the Core
// resolver and never consumes the idempotency key.
func TestCreateSurfaceFailsClosedOnUnsupportedRenderers(t *testing.T) {
	t.Parallel()
	client, repository, resolver := newSurfaceServer(t)
	ctx := context.Background()
	unimplemented := []surfacev1.SurfaceRenderer{
		surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_SERVICE,
		surfacev1.SurfaceRenderer_SURFACE_RENDERER_DECLARATIVE,
		surfacev1.SurfaceRenderer_SURFACE_RENDERER_REMOTE_NATIVE,
		surfacev1.SurfaceRenderer(99), // unknown enum value on the wire
	}
	for _, renderer := range unimplemented {
		response, err := client.CreateSurface(ctx, createRequest("renderer-key", renderer, 2, connectDevice))
		if err == nil {
			t.Fatalf("renderer %v started a surface: %s", renderer, response.Msg.GetSession().GetId())
		}
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("renderer %v verdict %v, want InvalidArgument", renderer, err)
		}
	}
	if resolver.calls != 0 {
		t.Fatal("rejected renderers reached the Core resolver")
	}
	if len(repository.requests) != 0 {
		t.Fatal("rejected renderers consumed the idempotency key")
	}
	// The untouched key still creates the surface with the implemented
	// renderer.
	response, err := client.CreateSurface(ctx, createRequest("renderer-key", surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE, 2, connectDevice))
	if err != nil || response.Msg.GetSession().GetId() == "" {
		t.Fatalf("key was poisoned by rejected renderers: %v", err)
	}
}

// TestCreateSurfaceBindsIdempotencyToTheTrustedDevice proves through the real
// handler that one key spans exactly one trusted device: the same key from a
// second device aborts (not NotFound), the first device keeps replaying its
// session, and a NaN pixel ratio — expressible in protobuf binary — is
// invalid before anything is consumed.
func TestCreateSurfaceBindsIdempotencyToTheTrustedDevice(t *testing.T) {
	t.Parallel()
	client, _, _ := newSurfaceServer(t)
	ctx := context.Background()
	first, err := client.CreateSurface(ctx, createRequest("device-key", surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED, 2, connectDevice))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Unspecified renderer defaults to web-bundle.
	if first.Msg.GetSession().GetRenderer() != surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE {
		t.Fatalf("unspecified renderer did not default to web-bundle: %v", first.Msg.GetSession().GetRenderer())
	}
	// Same key, different trusted device: a stable abort.
	_, conflict := client.CreateSurface(ctx, createRequest("device-key", surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED, 2, otherDevice))
	if connect.CodeOf(conflict) != connect.CodeAborted {
		t.Fatalf("cross-device replay verdict %v, want Aborted", conflict)
	}
	// Same key, same device: exact replay.
	replayed, err := client.CreateSurface(ctx, createRequest("device-key", surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED, 2, connectDevice))
	if err != nil || replayed.Msg.GetSession().GetId() != first.Msg.GetSession().GetId() {
		t.Fatalf("same-device replay failed: %v", err)
	}
	// NaN pixel ratio (binary protobuf can carry it) is InvalidArgument and
	// leaves the key unconsumed.
	if _, err := client.CreateSurface(ctx, createRequest("nan-key", surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED, math.NaN(), connectDevice)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("NaN viewport verdict %v, want InvalidArgument", err)
	}
	response, err := client.CreateSurface(ctx, createRequest("nan-key", surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED, 1, connectDevice))
	if err != nil {
		t.Fatalf("NaN attempt consumed the key: %v", err)
	}
	if response.Msg.GetSession().GetId() == "" {
		t.Fatal("post-NaN create returned no session")
	}
}

// TestCreateSurfaceClassifiesStoreFailures pins the transport split:
// an adapter-classified store outage is Unavailable, an unknown failure
// stays Internal.
func TestCreateSurfaceClassifiesStoreFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("store outage is unavailable", func(t *testing.T) {
		client, repository, _ := newSurfaceServer(t)
		repository.mu.Lock()
		repository.failWith = ports.ErrStoreUnavailable
		repository.mu.Unlock()
		_, err := client.CreateSurface(ctx, createRequest("outage-key", surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED, 2, connectDevice))
		if connect.CodeOf(err) != connect.CodeUnavailable {
			t.Fatalf("store outage verdict %v, want Unavailable", err)
		}
	})
	t.Run("unknown failure stays internal", func(t *testing.T) {
		client, repository, _ := newSurfaceServer(t)
		repository.mu.Lock()
		repository.failWith = errors.New("pool exhausted")
		repository.mu.Unlock()
		_, err := client.CreateSurface(ctx, createRequest("unknown-key", surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED, 2, connectDevice))
		if connect.CodeOf(err) != connect.CodeInternal {
			t.Fatalf("unknown failure verdict %v, want Internal", err)
		}
	})
}

var _ ports.SessionRepository = (*handlerRepository)(nil)
var _ ports.LaunchResolver = (*handlerResolver)(nil)

// TestCreateSurfaceReplayFailsClosedOnGrantEpochChange drives the ADR-0003 §3
// replay rule through the real Connect handler: a same-key replay re-resolves
// through Core, and once the installation grant epoch moved (a SetAppGrants
// mutation), the replay answers one fixed sanitized FailedPrecondition — no
// current or stored revision, no grant content — without rotating a usable
// credential onto the old-epoch session. A fresh key reopens under the new
// epoch.
func TestCreateSurfaceReplayFailsClosedOnGrantEpochChange(t *testing.T) {
	t.Parallel()
	client, repository, resolver := newSurfaceServer(t)
	ctx := context.Background()
	first, err := client.CreateSurface(ctx, createRequest("epoch-key", surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED, 2, connectDevice))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sessionID := first.Msg.GetSession().GetId()
	repository.mu.Lock()
	createdEpoch := repository.sessions[sessionID].InstallationGrantRevision
	repository.mu.Unlock()
	if createdEpoch != 2 {
		t.Fatalf("persisted epoch = %d, want resolver epoch 2", createdEpoch)
	}

	// Same epoch: the existing replay contract survives unchanged.
	replayed, err := client.CreateSurface(ctx, createRequest("epoch-key", surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED, 2, connectDevice))
	if err != nil || replayed.Msg.GetSession().GetId() != sessionID || replayed.Msg.GetSession().GetBridgeToken() == "" {
		t.Fatalf("same-epoch replay failed: %v", err)
	}

	// The grant mutates: Core now resolves epoch 3 for the same instance.
	resolver.descriptor.GrantRevision = 3
	_, err = client.CreateSurface(ctx, createRequest("epoch-key", surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED, 2, connectDevice))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("changed-epoch replay verdict %v, want FailedPrecondition", err)
	}
	message := err.Error()
	// The verdict is one fixed short message: the word "grants" is the
	// caller-facing fact, but no revision value or grant content may appear.
	for _, leak := range []string{"2", "3", "revision"} {
		if strings.Contains(message, leak) {
			t.Fatalf("replay error leaks %q: %s", leak, message)
		}
	}
	repository.mu.Lock()
	afterHash := repository.sessions[sessionID].BridgeTokenHash
	afterEpoch := repository.sessions[sessionID].InstallationGrantRevision
	repository.mu.Unlock()
	if afterHash != domain.HashBridgeToken(replayed.Msg.GetSession().GetBridgeToken()) {
		t.Fatal("changed-epoch replay rotated the session credential")
	}
	if afterEpoch != 2 {
		t.Fatalf("changed-epoch replay rewrote the persisted epoch to %d", afterEpoch)
	}

	// A fresh create key reopens under the new epoch.
	reopened, err := client.CreateSurface(ctx, createRequest("epoch-key-2", surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED, 2, connectDevice))
	if err != nil {
		t.Fatalf("fresh key create failed: %v", err)
	}
	repository.mu.Lock()
	reopenedEpoch := repository.sessions[reopened.Msg.GetSession().GetId()].InstallationGrantRevision
	repository.mu.Unlock()
	if reopenedEpoch != 3 {
		t.Fatalf("reopened epoch = %d, want 3", reopenedEpoch)
	}
}

// TestCreateSurfaceRejectsCorruptResolverRevision pins the transport verdict
// for an untrustworthy resolution (grant epoch below 1): a sanitized Internal,
// never a session row and never a client error.
func TestCreateSurfaceRejectsCorruptResolverRevision(t *testing.T) {
	t.Parallel()
	for _, revision := range []int64{0, -2} {
		client, repository, resolver := newSurfaceServer(t)
		resolver.descriptor.GrantRevision = revision
		_, err := client.CreateSurface(context.Background(), createRequest("corrupt-key", surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED, 2, connectDevice))
		if connect.CodeOf(err) != connect.CodeInternal {
			t.Fatalf("revision %d verdict %v, want Internal", revision, err)
		}
		if strings.Contains(err.Error(), "revision") {
			t.Fatalf("internal error leaks resolution detail: %s", err.Error())
		}
		if len(repository.sessions) != 0 || len(repository.requests) != 0 {
			t.Fatalf("revision %d persisted state", revision)
		}
	}
}
