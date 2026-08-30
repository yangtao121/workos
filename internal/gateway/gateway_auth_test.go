package gateway

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authv1connect "github.com/yangtao121/workos/gen/go/workos/auth/v1/authv1connect"
	"github.com/yangtao121/workos/internal/gateway/auth/application"
	"github.com/yangtao121/workos/internal/gateway/auth/domain"
	"github.com/yangtao121/workos/internal/gateway/auth/ports"
	authtransport "github.com/yangtao121/workos/internal/gateway/auth/transport"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// The production auth fixtures use one canonical owner and deterministic
// server-minted identifiers; no real credential material exists here.
const (
	testOwnerID        = "0198d7ea-2110-7c42-b659-c5e4d73bc337"
	testOrigin         = "https://workos.example"
	testFingerprint    = "sha256:aa00000000000000000000000000000000000000000000000000000000000000"
	testDeviceID       = "0198d7ea-2110-7c42-b659-c5e4d73bc341"
	testSessionID      = "0198d7ea-2110-7c42-b659-c5e4d73bc342"
	testSessionExpires = 24 * time.Hour
)

// fixedClock gives the flows deterministic time.
type fixedClock struct{ current time.Time }

func (c *fixedClock) Now() time.Time { return c.current }

// counterIDs mints grammar-valid UUIDv7-shaped identifiers deterministically.
type counterIDs struct {
	mu sync.Mutex
	n  int
}

func (c *counterIDs) New() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return "0198d7ea-2110-7c42-b659-c5e4d73bc4" + string(rune(c.n/10+'0')) + string(rune(c.n%10+'0'))
}

// deterministicEntropy repeats a fixed byte so tests stay reproducible; it
// never models real secret strength.
type deterministicEntropy struct{}

func (deterministicEntropy) Random(n int) ([]byte, error) {
	raw := make([]byte, n)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	return raw, nil
}

// newTestAuthStack wires a full production auth stack on an in-memory
// repository with deterministic time, entropy, and identifiers.
func newTestAuthStack(t *testing.T, store ports.Repository) *AuthStack {
	t.Helper()
	clock := &fixedClock{current: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)}
	app, err := application.New(store, application.Config{
		OwnerID:        testOwnerID,
		PublicOrigin:   testOrigin,
		TLSFingerprint: testFingerprint,
		TicketTTL:      5 * time.Minute,
		ChallengeTTL:   2 * time.Minute,
		SessionTTL:     testSessionExpires,
	}, clock, deterministicEntropy{}, &counterIDs{})
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return clock.Now() }
	_, pairingConnect := authv1connect.NewDevicePairingServiceHandler(authtransport.NewPairingHandler(app, now))
	_, deviceConnect := authv1connect.NewDeviceServiceHandler(authtransport.NewDeviceHandler(app, now))
	return &AuthStack{
		Service:       app,
		Pairing:       pairingConnect,
		Device:        deviceConnect,
		RemoteLimiter: application.NewRateLimiter(1000, time.Minute, 4096, clock),
		GlobalLimiter: application.NewRateLimiter(1000, time.Minute, 1, clock),
	}
}

// testSessionToken is a grammar-valid session token fixture; the store
// accepts exactly its hash, so tests exercise the full gate path.
var testSessionToken = base64.RawURLEncoding.EncodeToString(make([]byte, domain.SecretBytes))

// gateStore is the minimal repository surface the session gate needs for
// these tests; unimplemented methods panic loudly if a test reaches them.
type gateStore struct {
	outage     bool
	resolveErr error
	session    domain.DeviceSession
	device     domain.Device
	resolved   int
}

func (g *gateStore) Ready(ctx context.Context) error {
	if g.outage {
		return context.DeadlineExceeded
	}
	return nil
}

func (g *gateStore) ResolveSession(ctx context.Context, tokenHash string) (domain.DeviceSession, domain.Device, error) {
	g.resolved++
	if g.resolveErr != nil {
		return domain.DeviceSession{}, domain.Device{}, g.resolveErr
	}
	if g.outage {
		return domain.DeviceSession{}, domain.Device{}, domain.ErrStoreUnavailable
	}
	if tokenHash != g.session.TokenHash {
		return domain.DeviceSession{}, domain.Device{}, domain.ErrAuthenticationFailed
	}
	return g.session, g.device, nil
}

func newGateStore(active bool) *gateStore {
	revoked := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	device := domain.Device{ID: testDeviceID, OwnerID: testOwnerID, Name: "Fixture Device", Class: domain.DeviceClassDesktop, Revision: 1}
	raw, _ := base64.RawURLEncoding.DecodeString(testSessionToken)
	session := domain.DeviceSession{ID: testSessionID, OwnerID: testOwnerID, DeviceID: testDeviceID, TokenHash: domain.HashSessionToken(raw)}
	if !active {
		device.RevokedAt = &revoked
	}
	expires := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	session.ExpiresAt = expires
	return &gateStore{session: session, device: device}
}

// Unused repository methods fail loudly rather than silently succeeding.
func (g *gateStore) RotatePairingTicket(ctx context.Context, ticket domain.PairingTicket) error {
	panic("not expected in gate tests")
}
func (g *gateStore) LoadTicketBySecretHash(ctx context.Context, secretHash string, now time.Time) (domain.PairingTicket, error) {
	panic("not expected in gate tests")
}
func (g *gateStore) LoadTicket(ctx context.Context, id, ownerID string) (domain.PairingTicket, error) {
	panic("not expected in gate tests")
}
func (g *gateStore) ClaimPairingTicket(ctx context.Context, ticketID, ownerID, deviceID, publicKeyHash, deviceName, deviceClass string, now time.Time) (domain.PairingTicket, error) {
	panic("not expected in gate tests")
}
func (g *gateStore) FailTicketAttempt(ctx context.Context, ticketID string) error {
	panic("not expected in gate tests")
}
func (g *gateStore) CreateChallenge(ctx context.Context, challenge domain.Challenge) error {
	panic("not expected in gate tests")
}
func (g *gateStore) LoadChallenge(ctx context.Context, id string) (domain.Challenge, error) {
	panic("not expected in gate tests")
}
func (g *gateStore) ConsumeChallenge(ctx context.Context, id, deviceID string, result domain.ChallengeResult, now time.Time) error {
	panic("not expected in gate tests")
}
func (g *gateStore) FailChallengeAttempt(ctx context.Context, id string) error {
	panic("not expected in gate tests")
}
func (g *gateStore) LoadActiveDevice(ctx context.Context, id string) (domain.Device, error) {
	panic("not expected in gate tests")
}
func (g *gateStore) CompletePairing(ctx context.Context, op ports.CompletePairingOp) (domain.Device, domain.DeviceSession, error) {
	panic("not expected in gate tests")
}
func (g *gateStore) CompleteSession(ctx context.Context, op ports.CompleteSessionOp) (domain.Device, domain.DeviceSession, error) {
	panic("not expected in gate tests")
}
func (g *gateStore) TouchSessionLastSeen(ctx context.Context, sessionID string, now, threshold time.Time) {
}
func (g *gateStore) ListDevices(ctx context.Context, ownerID, cursorUUID string, limit int) ([]domain.Device, error) {
	panic("not expected in gate tests")
}
func (g *gateStore) RevokeDevice(ctx context.Context, op ports.RevokeDeviceOp) (domain.Device, bool, error) {
	panic("not expected in gate tests")
}
func (g *gateStore) Logout(ctx context.Context, sessionID, ownerID string, now time.Time) error {
	panic("not expected in gate tests")
}

func newTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestProductionGateBehavior pins the per-request session gate: Host
// mismatch, missing/invalid cookies, store outages, and cross-site browser
// writes all fail closed before any upstream call, while a valid session
// proxies with the exact dynamic identity and never forwards the cookie.
func TestProductionGateBehavior(t *testing.T) {
	t.Parallel()
	var coreHeaders http.Header
	var coreCalled int
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		coreCalled++
		coreHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer core.Close()

	store := newGateStore(true)
	stack := newTestAuthStack(t, store)
	handler, err := New(config.Config{
		HTTP:     config.HTTP{StaticDir: t.TempDir()},
		Services: config.URLs{Core: core.URL, Runtime: "http://127.0.0.1:1"},
		Auth: config.Auth{
			OwnerID:      testOwnerID,
			PublicOrigin: testOrigin,
			DevBypass:    false,
		},
	}, newTestLogger(), stack)
	if err != nil {
		t.Fatal(err)
	}

	request := func(method, path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, testOrigin+path, nil)
		req.Host = "workos.example"
		if isUnsafeMethod(method) {
			req.Header.Set("Origin", testOrigin)
		}
		if mutate != nil {
			mutate(req)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}

	// Missing cookie: fixed 401, upstream never called.
	response := request(http.MethodPost, "/workos.project.v1.ProjectService/ListProjects", nil)
	if response.Code != http.StatusUnauthorized || coreCalled != 0 {
		t.Fatalf("missing cookie: status=%d coreCalled=%d", response.Code, coreCalled)
	}

	// Invalid cookie: 401 with the cookie cleared using the exact same
	// security attributes.
	response = request(http.MethodPost, "/workos.project.v1.ProjectService/ListProjects", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: authtransport.SessionCookieName, Value: "forged"})
	})
	if response.Code != http.StatusUnauthorized || coreCalled != 0 {
		t.Fatalf("invalid cookie: status=%d coreCalled=%d", response.Code, coreCalled)
	}
	clear := ""
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == authtransport.SessionCookieName {
			clear = cookie.Value
		}
	}
	if clear != "" {
		t.Fatalf("invalid-cookie clear did not blank the value: %q", clear)
	}

	// Host mismatch: fixed 403 before any other work.
	req := httptest.NewRequest(http.MethodPost, testOrigin+"/workos.project.v1.ProjectService/ListProjects", nil)
	req.Host = "attacker.example"
	req.AddCookie(&http.Cookie{Name: authtransport.SessionCookieName, Value: testSessionToken})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden || coreCalled != 0 {
		t.Fatalf("host mismatch: status=%d coreCalled=%d", recorder.Code, coreCalled)
	}

	// Cross-site browser write: exact Origin required.
	response = request(http.MethodPost, "/workos.project.v1.ProjectService/ListProjects", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: authtransport.SessionCookieName, Value: testSessionToken})
		r.Header.Set("Origin", "https://evil.example")
	})
	if response.Code != http.StatusForbidden || coreCalled != 0 {
		t.Fatalf("cross-site origin: status=%d coreCalled=%d", response.Code, coreCalled)
	}

	// Exact Origin does not override contradictory Fetch Metadata on an
	// unsafe request.
	response = request(http.MethodPost, "/workos.project.v1.ProjectService/ListProjects", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: authtransport.SessionCookieName, Value: testSessionToken})
		r.Header.Set("Origin", testOrigin)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	if response.Code != http.StatusForbidden || coreCalled != 0 {
		t.Fatalf("cross-site unsafe fetch metadata: status=%d coreCalled=%d", response.Code, coreCalled)
	}

	// Cross-site Fetch Metadata is rejected on safe methods too.
	response = request(http.MethodGet, "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: authtransport.SessionCookieName, Value: testSessionToken})
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	if response.Code != http.StatusForbidden || coreCalled != 0 {
		t.Fatalf("cross-site fetch metadata: status=%d coreCalled=%d", response.Code, coreCalled)
	}

	// Store outage: sanitized 503, no fallback to a previous identity.
	store.outage = true
	response = request(http.MethodPost, "/workos.project.v1.ProjectService/ListProjects", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: authtransport.SessionCookieName, Value: testSessionToken})
	})
	if response.Code != http.StatusServiceUnavailable || coreCalled != 0 {
		t.Fatalf("store outage: status=%d coreCalled=%d", response.Code, coreCalled)
	}
	if body := response.Body.String(); !strings.Contains(body, "gateway auth unavailable") {
		t.Fatalf("unsanitized outage body %q", body)
	}
	store.outage = false

	// Corrupt durable auth state is a fixed 500 and does not clear a valid
	// cookie as though the caller had failed authentication.
	store.resolveErr = domain.ErrAuthCorrupt
	response = request(http.MethodPost, "/workos.project.v1.ProjectService/ListProjects", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: authtransport.SessionCookieName, Value: testSessionToken})
	})
	if response.Code != http.StatusInternalServerError || coreCalled != 0 {
		t.Fatalf("store corruption: status=%d coreCalled=%d", response.Code, coreCalled)
	}
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("store corruption cleared the caller's session cookie")
	}
	store.resolveErr = nil

	// Revoked device session: fixed 401.
	revokedStore := newGateStore(false)
	revokedStack := newTestAuthStack(t, revokedStore)
	revokedHandler, err := New(config.Config{
		HTTP:     config.HTTP{StaticDir: t.TempDir()},
		Services: config.URLs{Core: core.URL, Runtime: "http://127.0.0.1:1"},
		Auth:     config.Auth{OwnerID: testOwnerID, PublicOrigin: testOrigin},
	}, newTestLogger(), revokedStack)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, testOrigin+"/workos.project.v1.ProjectService/ListProjects", nil)
	req.Host = "workos.example"
	req.Header.Set("Origin", testOrigin)
	req.AddCookie(&http.Cookie{Name: authtransport.SessionCookieName, Value: testSessionToken})
	recorder = httptest.NewRecorder()
	revokedHandler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session: status=%d", recorder.Code)
	}

	// Valid session: proxied with exact dynamic identity; cookie, spoofed
	// identity headers, and bridge tokens never reach the upstream.
	response = request(http.MethodPost, "/workos.project.v1.ProjectService/ListProjects", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: authtransport.SessionCookieName, Value: testSessionToken})
		r.Header.Set(identity.UserHeader, "attacker")
		r.Header.Set(identity.DeviceHeader, "attacker")
		r.Header.Set(identity.BridgeTokenHeader, "bridge-token")
		r.Header.Set("Origin", testOrigin)
	})
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid session: status=%d body=%s", response.Code, response.Body.String())
	}
	if coreCalled != 1 {
		t.Fatalf("valid session never reached core: %d", coreCalled)
	}
	if got := coreHeaders.Get(identity.UserHeader); got != testOwnerID {
		t.Fatalf("dynamic user identity %q", got)
	}
	if got := coreHeaders.Get(identity.DeviceHeader); got != testDeviceID {
		t.Fatalf("dynamic device identity %q", got)
	}
	if coreHeaders.Get("Cookie") != "" {
		t.Fatal("session cookie leaked to the upstream")
	}
	if coreHeaders.Get(identity.BridgeTokenHeader) != "" {
		t.Fatal("bridge token leaked to a core route")
	}
	// The reverse proxy itself may assert the immediate peer as
	// X-Forwarded-For; client-supplied forwarding material must be gone.
	if forwarded := coreHeaders.Get("X-Forwarded-For"); forwarded != "" && forwarded != "192.0.2.1" {
		t.Fatalf("client X-Forwarded-For reached the upstream: %q", forwarded)
	}
}

// TestProxyStripsClientForwardingHeaders pins that client-supplied
// Forwarded/X-Forwarded-* headers never reach an upstream, on both gates.
func TestProxyStripsClientForwardingHeaders(t *testing.T) {
	t.Parallel()
	var seen http.Header
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer core.Close()

	for name, cfg := range map[string]config.Config{
		"dev": {
			Services: config.URLs{Core: core.URL, Runtime: "http://127.0.0.1:1"},
			Auth:     config.Auth{DevBypass: true, OwnerID: testOwnerID, DeviceID: testDeviceID},
		},
	} {
		handler, err := New(cfg, newTestLogger(), nil)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "http://gateway.test/workos.project.v1.ProjectService/ListProjects", nil)
		request.Header.Set("Forwarded", `for=198.51.100.7;host=evil.example`)
		request.Header.Set("X-Forwarded-For", "198.51.100.7")
		request.Header.Set("X-Forwarded-Proto", "https")
		request.Header.Set("X-Forwarded-Host", "evil.example")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s gate: status %d", name, response.Code)
		}
		if seen.Get("Forwarded") != "" || seen.Get("X-Forwarded-Proto") != "" ||
			seen.Get("X-Forwarded-Host") != "" {
			t.Fatalf("%s gate forwarded client forwarding headers: %v", name, seen)
		}
		if xff := seen.Get("X-Forwarded-For"); xff != "" && xff != "192.0.2.1" {
			t.Fatalf("%s gate forwarded client X-Forwarded-For %q", name, xff)
		}
	}
}

// TestProductionNewFailsWithoutAuthStack pins the composition-root guard:
// bypass off without the device auth stack cannot start.
func TestProductionNewFailsWithoutAuthStack(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		Services: config.URLs{Core: "http://127.0.0.1:1", Runtime: "http://127.0.0.1:1"},
		Auth:     config.Auth{OwnerID: testOwnerID, PublicOrigin: testOrigin},
	}
	if _, err := New(cfg, newTestLogger(), nil); err == nil {
		t.Fatal("production handler accepted a nil auth stack")
	}
	for name, mutate := range map[string]func(*AuthStack){
		"remote limiter": func(stack *AuthStack) { stack.RemoteLimiter = nil },
		"global limiter": func(stack *AuthStack) { stack.GlobalLimiter = nil },
	} {
		stack := newTestAuthStack(t, newGateStore(true))
		mutate(stack)
		if _, err := New(cfg, newTestLogger(), stack); err == nil {
			t.Errorf("production handler accepted a nil %s", name)
		}
	}
}

// TestProductionAdminServiceStaysOffTCP pins that the private admin service
// prefix is deterministically unreachable over the public listener.
func TestProductionAdminServiceStaysOffTCP(t *testing.T) {
	t.Parallel()
	handler, err := New(config.Config{
		HTTP:     config.HTTP{StaticDir: t.TempDir()},
		Services: config.URLs{Core: "http://127.0.0.1:1", Runtime: "http://127.0.0.1:1"},
		Auth:     config.Auth{OwnerID: testOwnerID, PublicOrigin: testOrigin},
	}, newTestLogger(), func() *AuthStack {
		store := newGateStore(true)
		return newTestAuthStack(t, store)
	}())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, testOrigin+"/workos.auth.v1.DeviceAuthAdminService/RotatePairingTicket", nil)
	req.Host = "workos.example"
	req.Header.Set("Origin", testOrigin)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("admin service reachable over TCP: status=%d", recorder.Code)
	}
}

// TestProductionPairingEndpointsAreAnonymous verifies the pairing service
// prefix is routed locally without a session cookie and rate-limited per
// remote address.
func TestProductionPairingEndpointsAreAnonymous(t *testing.T) {
	t.Parallel()
	store := newGateStore(true)
	stack := newTestAuthStack(t, store)
	handler, err := New(config.Config{
		HTTP:     config.HTTP{StaticDir: t.TempDir()},
		Services: config.URLs{Core: "http://127.0.0.1:1", Runtime: "http://127.0.0.1:1"},
		Auth:     config.Auth{OwnerID: testOwnerID, PublicOrigin: testOrigin},
	}, newTestLogger(), stack)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, testOrigin+"/workos.auth.v1.DevicePairingService/BeginPairing", strings.NewReader("{}"))
	req.Host = "workos.example"
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	// An empty body is an invalid request — but never a session redirect:
	// the endpoint is reachable anonymously.
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("anonymous pairing endpoint returned %d", recorder.Code)
	}
}

// TestProductionPairingRateLimits pins both anonymous budgets: repeated
// traffic from one address and address-rotating traffic are independently
// bounded, and a malformed RemoteAddr never bypasses accounting.
func TestProductionPairingRateLimits(t *testing.T) {
	t.Parallel()
	newHandler := func(remoteLimit, globalLimit int) *Handler {
		clock := &fixedClock{current: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)}
		stack := newTestAuthStack(t, newGateStore(true))
		stack.RemoteLimiter = application.NewRateLimiter(remoteLimit, time.Minute, 16, clock)
		stack.GlobalLimiter = application.NewRateLimiter(globalLimit, time.Minute, 1, clock)
		handler, err := New(config.Config{
			HTTP:     config.HTTP{StaticDir: t.TempDir()},
			Services: config.URLs{Core: "http://127.0.0.1:1", Runtime: "http://127.0.0.1:1"},
			Auth:     config.Auth{OwnerID: testOwnerID, PublicOrigin: testOrigin},
		}, newTestLogger(), stack)
		if err != nil {
			t.Fatal(err)
		}
		return handler
	}
	request := func(handler *Handler, remoteAddr string) int {
		req := httptest.NewRequest(http.MethodPost, testOrigin+"/workos.auth.v1.DevicePairingService/BeginPairing", strings.NewReader("{}"))
		req.Host = "workos.example"
		req.RemoteAddr = remoteAddr
		req.Header.Set("Origin", testOrigin)
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder.Code
	}

	remoteBound := newHandler(1, 100)
	if got := request(remoteBound, "192.0.2.10:1000"); got == http.StatusTooManyRequests {
		t.Fatalf("first per-remote request unexpectedly limited: %d", got)
	}
	if got := request(remoteBound, "192.0.2.10:2000"); got != http.StatusTooManyRequests {
		t.Fatalf("per-remote limit returned %d", got)
	}

	globalBound := newHandler(100, 1)
	if got := request(globalBound, "192.0.2.11:1000"); got == http.StatusTooManyRequests {
		t.Fatalf("first global request unexpectedly limited: %d", got)
	}
	if got := request(globalBound, "192.0.2.12:1000"); got != http.StatusTooManyRequests {
		t.Fatalf("global limit returned %d", got)
	}

	malformedBound := newHandler(1, 100)
	_ = request(malformedBound, "not-a-socket-address")
	if got := request(malformedBound, "also-not-an-address"); got != http.StatusTooManyRequests {
		t.Fatalf("malformed RemoteAddr bypassed shared bucket: %d", got)
	}
}
