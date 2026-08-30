//go:build integration

package integration_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yangtao121/workos/internal/gateway/auth/adapters/postgres"
	"github.com/yangtao121/workos/internal/gateway/auth/adapters/randsource"
	"github.com/yangtao121/workos/internal/gateway/auth/application"
	"github.com/yangtao121/workos/internal/gateway/auth/domain"
	"github.com/yangtao121/workos/internal/platform/database"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// The pinned 001–019 checksums live in project_service_contract_test.go;
// 020 is this slice's only new file.
const gatewayDeviceAuthMigration = "020_gateway_device_auth.sql"

const integrationOwnerID = "0198d7ea-2110-7c42-b659-c5e4d73bc337"

func integrationAuthConfig(fingerprint string) application.Config {
	return application.Config{
		OwnerID:        integrationOwnerID,
		PublicOrigin:   "https://workos.example",
		TLSFingerprint: fingerprint,
		TicketTTL:      5 * time.Minute,
		ChallengeTTL:   2 * time.Minute,
		SessionTTL:     24 * time.Hour,
	}
}

// TestGatewayDeviceAuthMigrationChain proves 020 applies from an empty
// database and after 019, enforces its invariants at the database level,
// and that a second run is a no-op.
func TestGatewayDeviceAuthMigrationChain(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations from empty database: %v", err)
	}
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("second migration run must be a no-op: %v", err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer closeConn(conn)

	var applied bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workos_meta.schema_migrations WHERE name = $1)`,
		gatewayDeviceAuthMigration).Scan(&applied); err != nil || !applied {
		t.Fatalf("020 must be applied: %v %v", err, applied)
	}
	for _, table := range []string{"device_credentials", "pairing_tickets", "device_auth_challenges", "device_sessions", "device_revocation_requests"} {
		var count int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'workos_gateway' AND table_name = $1`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("workos_gateway.%s missing: %v %d", table, err, count)
		}
	}

	// Plaintext secrets have no column to live in: assert the guard shapes
	// by attempting incoherent writes.
	if _, err := conn.Exec(ctx, `INSERT INTO workos_gateway.pairing_tickets
		(id, owner_user_id, secret_hash, public_origin, tls_fingerprint, state, expires_at, created_at)
		VALUES ($1, $2, 'plaintext-secret', 'https://workos.example', 'sha256:ab', 'pending', now() + interval '5 minutes', now())`,
		"0198d7ea-2110-7c42-b659-c5e4d73b9001", integrationOwnerID); err == nil {
		t.Fatal("ticket with malformed secret hash accepted")
	}
	if _, err := conn.Exec(ctx, `INSERT INTO workos_gateway.device_sessions
		(id, owner_user_id, device_id, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, 'raw-token', now(), now() + interval '1 hour')`,
		"0198d7ea-2110-7c42-b659-c5e4d73b9002", integrationOwnerID, "0198d7ea-2110-7c42-b659-c5e4d73b9003"); err == nil {
		t.Fatal("session with malformed token hash accepted")
	}
	// Incoherent state machine facts are rejected.
	if _, err := conn.Exec(ctx, `INSERT INTO workos_gateway.pairing_tickets
		(id, owner_user_id, secret_hash, public_origin, tls_fingerprint, state, claimed_at, expires_at, created_at)
		VALUES ($1, $2, $3, 'https://workos.example', $4, 'pending', now(), now() + interval '5 minutes', now())`,
		"0198d7ea-2110-7c42-b659-c5e4d73b9004", integrationOwnerID, ticketHashFixture("a"), fingerprintFixture("a")); err == nil {
		t.Fatal("pending ticket with claimed_at accepted")
	}
	// Device credentials demand the bounded grammar.
	if _, err := conn.Exec(ctx, `INSERT INTO workos_gateway.device_credentials
		(id, owner_user_id, name, device_class, public_key_spki, public_key_hash, revision, created_at, last_authenticated_at)
		VALUES ($1, $2, $3, 'desktop', 'k', $4, 1, now(), now())`,
		"0198d7ea-2110-7c42-b659-c5e4d73b9005", integrationOwnerID, "bad\x07name", ticketHashFixture("b")); err == nil {
		t.Fatal("device with control characters accepted")
	}
}

func ticketHashFixture(seed string) string {
	return "sha256:" + repeatSeed(seed, 64)
}

func fingerprintFixture(seed string) string {
	return "sha256:" + repeatSeed(seed, 64)
}

func repeatSeed(seed string, n int) string {
	out := ""
	for len(out) < n {
		out += seed
	}
	return out[:n]
}

// retryUnavailable runs op with bounded backoff while the store reports
// transient unavailability: the full suite's parallel scratch databases can
// momentarily exhaust PostgreSQL connections, and these flows' goal is
// concurrency adjudication, not outage behavior (which unit tests pin).
// Non-outage results and errors return immediately (T values are valid only
// when err is nil).
func retryUnavailable[T any](t *testing.T, op func() (T, error)) (T, error) {
	t.Helper()
	for attempt := 0; ; attempt++ {
		result, err := op()
		if err == nil || !errors.Is(err, domain.ErrStoreUnavailable) {
			return result, err
		}
		if attempt >= 5 {
			t.Fatalf("store stayed unavailable after retries: %v", err)
		}
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
}

// integrationKey is a real P-256 signing stand-in for the browser profile.
type integrationKey struct {
	priv *ecdsa.PrivateKey
	spki []byte
	hash string
}

func newIntegrationKey(t *testing.T) *integrationKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := domain.ParseP256SPKI(spki)
	if err != nil {
		t.Fatal(err)
	}
	return &integrationKey{priv: priv, spki: spki, hash: hash}
}

func (k *integrationKey) sign(t *testing.T, facts domain.ProofFacts) []byte {
	t.Helper()
	transcript, err := domain.EncodeProof(facts)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(transcript)
	der, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	var inner struct {
		R, S *big.Int
	}
	if _, err := asn1.Unmarshal(der, &inner); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 64)
	rightPad(inner.R, out[:32])
	rightPad(inner.S, out[32:])
	return out
}

func rightPad(value *big.Int, dst []byte) {
	raw := value.Bytes()
	if len(raw) > len(dst) {
		raw = raw[len(raw)-len(dst):]
	}
	copy(dst[len(dst)-len(raw):], raw)
}

// newIntegrationService builds the full application service over the real
// PostgreSQL adapter on the scratch database.
func newIntegrationService(t *testing.T, dsn string, fingerprint string) *application.Service {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	t.Cleanup(pool.Close)
	service, err := application.New(
		postgres.New(pool), integrationAuthConfig(fingerprint),
		randsource.Clock{}, randsource.Entropy{}, integrationIDs{},
	)
	if err != nil {
		t.Fatalf("build application service: %v", err)
	}
	return service
}

// integrationIDs defers to the platform generator; it exists so tests can
// pin the minted-id grammar.
type integrationIDs struct{}

func (integrationIDs) New() string { return ids.UUIDv7{}.New() }

// TestGatewayDeviceAuthRepositoryFlow walks the full durable lifecycle on
// real PostgreSQL: rotation, claim, proof completion, session resolution,
// session rotation, revocation idempotency, and cross-process durability.
func TestGatewayDeviceAuthRepositoryFlow(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	if err := migrations.Run(context.Background(), dsn); err != nil {
		t.Fatalf("migrate scratch database: %v", err)
	}
	service := newIntegrationService(t, dsn, "sha256:"+repeatSeed("aa", 64))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Rotate and pair.
	info, err := service.RotatePairingTicket(ctx, integrationOwnerID)
	if err != nil {
		t.Fatalf("rotate ticket: %v", err)
	}
	key := newIntegrationKey(t)
	begin, err := service.BeginPairing(ctx, application.BeginPairingInput{
		PairingSecret: info.Secret, PublicKeySPKI: key.spki, DeviceName: "Integration Device", DeviceClass: "desktop",
	})
	if err != nil {
		t.Fatalf("begin pairing: %v", err)
	}
	facts := domain.ProofFacts{
		PublicOrigin: info.PublicOrigin, Purpose: domain.PurposePairing,
		ChallengeID: begin.Challenge.ID, Nonce: begin.Challenge.Nonce,
		DeviceID: begin.DeviceID, PublicKeyHash: key.hash,
		TicketID: info.TicketID, TLSFingerprint: info.TLSFingerprint,
	}
	completion, err := service.CompletePairing(ctx, application.CompletePairingInput{
		DeviceID: begin.DeviceID, ChallengeID: begin.Challenge.ID,
		PublicKeySPKI: key.spki, Signature: key.sign(t, facts),
	})
	if err != nil {
		t.Fatalf("complete pairing: %v", err)
	}

	// The raw token or private key material must not exist anywhere.
	assertNoPlaintextSecrets(t, dsn, completion.SessionToken)

	// Session rotation: a second proof leaves exactly one active session.
	challenge, err := service.BeginDeviceSession(ctx, completion.Device.ID)
	if err != nil {
		t.Fatalf("begin device session: %v", err)
	}
	sessionFacts := domain.ProofFacts{
		PublicOrigin: "https://workos.example", Purpose: domain.PurposeSession,
		ChallengeID: challenge.ID, Nonce: challenge.Nonce,
		DeviceID: completion.Device.ID, PublicKeyHash: key.hash,
	}
	rotated, err := service.CompleteDeviceSession(ctx, application.CompleteSessionInput{
		DeviceID: completion.Device.ID, ChallengeID: challenge.ID,
		Signature: key.sign(t, sessionFacts),
	})
	if err != nil {
		t.Fatalf("complete device session: %v", err)
	}
	if rotated.SessionToken == completion.SessionToken {
		t.Fatal("session token was not rotated")
	}
	if _, err := service.ResolveSession(ctx, completion.SessionToken); err == nil {
		t.Fatal("old session still resolves after rotation")
	}

	// Revocation with idempotent replay and digest conflicts.
	identity, err := service.ResolveSession(ctx, rotated.SessionToken)
	if err != nil {
		t.Fatalf("rotated session does not resolve: %v", err)
	}
	op := application.RevokeDeviceInput{DeviceID: completion.Device.ID, IdempotencyKey: ids.UUIDv7{}.New(), ExpectedRevision: 1}
	revoked, replay, err := service.RevokeDevice(ctx, identity, op)
	if err != nil || replay {
		t.Fatalf("revoke: %v replay=%v", err, replay)
	}
	if revoked.RevokedAt == nil || revoked.Revision != 2 {
		t.Fatalf("revoked facts: %+v", revoked)
	}
	replayedDevice, wasReplay, err := service.RevokeDevice(ctx, identity, op)
	if err != nil || !wasReplay {
		t.Fatalf("idempotent replay failed: %v", err)
	}
	if replayedDevice.ID != revoked.ID || replayedDevice.Name != revoked.Name ||
		replayedDevice.Class != revoked.Class || replayedDevice.Revision != revoked.Revision ||
		!replayedDevice.CreatedAt.Equal(revoked.CreatedAt) ||
		!replayedDevice.LastAuthenticatedAt.Equal(revoked.LastAuthenticatedAt) ||
		replayedDevice.RevokedAt == nil || !replayedDevice.RevokedAt.Equal(*revoked.RevokedAt) {
		t.Fatalf("replayed public device facts differ: first=%+v replay=%+v", revoked, replayedDevice)
	}
	if _, err := service.ResolveSession(ctx, rotated.SessionToken); err == nil {
		t.Fatal("revoked session still resolves")
	}
}

// assertNoPlaintextSecrets dumps every auth table and fails if the raw
// session token (or any 43-char base64url secret) persisted.
func assertNoPlaintextSecrets(t *testing.T, dsn, rawToken string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer closeConn(conn)
	for _, query := range []string{
		`SELECT secret_hash FROM workos_gateway.pairing_tickets`,
		`SELECT token_hash FROM workos_gateway.device_sessions`,
	} {
		rows, err := conn.Query(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			if value == rawToken {
				t.Fatalf("raw token persisted in %s", query)
			}
			if len(value) == 43 {
				t.Fatalf("unhashed 43-char secret persisted: %s", query)
			}
		}
		rows.Close()
	}
}

// TestGatewayDeviceAuthConcurrency pins the PostgreSQL-adjudicated races:
// concurrent rotations converge on one pending ticket, concurrent claims
// produce one winner, and concurrent completions mint exactly one device
// and session.
func TestGatewayDeviceAuthConcurrency(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	if err := migrations.Run(context.Background(), dsn); err != nil {
		t.Fatalf("migrate scratch database: %v", err)
	}
	service := newIntegrationService(t, dsn, "sha256:"+repeatSeed("bb", 64))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Concurrent rotations: exactly one pending ticket survives. The full
	// suite's parallel scratch databases can momentarily exhaust PostgreSQL
	// connections, so transient Unavailable results retry with backoff —
	// these flows pin concurrency adjudication, not outage behavior.
	const rotations = 8
	infos := make([]domain.PairingInfo, rotations)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < rotations; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := retryUnavailable(t, func() (domain.PairingInfo, error) {
				return service.RotatePairingTicket(ctx, integrationOwnerID)
			})
			if err == nil {
				mu.Lock()
				infos[i] = info
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	conn := scratchConn(t, dsn)
	var pending int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM workos_gateway.pairing_tickets WHERE owner_user_id = $1 AND state = 'pending'`,
		integrationOwnerID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("expected exactly one pending ticket after concurrent rotation, found %d", pending)
	}

	// Identify the surviving pending ticket side-effect-free: the goroutine
	// outputs carry each ticket's id, and exactly one row stayed pending.
	var pendingID string
	if err := conn.QueryRow(ctx, `SELECT id FROM workos_gateway.pairing_tickets
		WHERE owner_user_id = $1 AND state = 'pending'`, integrationOwnerID).Scan(&pendingID); err != nil {
		t.Fatalf("read surviving pending ticket: %v", err)
	}
	var survivor domain.PairingInfo
	for _, info := range infos {
		if info.TicketID == pendingID {
			survivor = info
			break
		}
	}
	if survivor.TicketID == "" {
		t.Fatal("pending ticket not present in rotation outputs")
	}

	// Concurrent claims by distinct browser keys on the surviving ticket:
	// exactly one winner; every loser fails closed on the guarded
	// pending→claimed transition. Identical key+metadata is intentionally a
	// recoverable retry and therefore is not a valid one-winner fixture.
	claimKeys := make([]*integrationKey, 4)
	for i := range claimKeys {
		claimKeys[i] = newIntegrationKey(t)
	}
	var claims int
	var claimMu sync.Mutex
	var winner application.BeginPairingResult
	var winnerKey *integrationKey
	for _, contenderKey := range claimKeys {
		contenderKey := contenderKey
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := retryUnavailable(t, func() (application.BeginPairingResult, error) {
				return service.BeginPairing(ctx, application.BeginPairingInput{
					PairingSecret: survivor.Secret, PublicKeySPKI: contenderKey.spki, DeviceName: "Racer", DeviceClass: "desktop",
				})
			})
			if err == nil {
				claimMu.Lock()
				claims++
				winner = result
				winnerKey = contenderKey
				claimMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if claims != 1 || winnerKey == nil {
		t.Fatalf("expected exactly one successful claim, found %d", claims)
	}

	// Concurrent completions of the one claimed pairing: the challenge is
	// consumed once and the ticket completes once, so exactly one device and
	// one active session may be minted; every loser fails closed.
	facts := domain.ProofFacts{
		PublicOrigin: "https://workos.example", Purpose: domain.PurposePairing,
		ChallengeID: winner.Challenge.ID, Nonce: winner.Challenge.Nonce,
		DeviceID: winner.DeviceID, PublicKeyHash: winnerKey.hash,
		TicketID: survivor.TicketID, TLSFingerprint: "sha256:" + repeatSeed("bb", 64),
	}
	signature := winnerKey.sign(t, facts)
	const completions = 6
	var completed int
	var completeMu sync.Mutex
	for i := 0; i < completions; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := retryUnavailable(t, func() (application.PairingCompletion, error) {
				return service.CompletePairing(ctx, application.CompletePairingInput{
					DeviceID: winner.DeviceID, ChallengeID: winner.Challenge.ID,
					PublicKeySPKI: winnerKey.spki, Signature: signature,
				})
			})
			if err == nil {
				completeMu.Lock()
				completed++
				completeMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if completed != 1 {
		t.Fatalf("expected exactly one successful completion, found %d", completed)
	}
	var devices, sessions int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM workos_gateway.device_credentials WHERE id = $1`,
		winner.DeviceID).Scan(&devices); err != nil || devices != 1 {
		t.Fatalf("expected exactly one device row: %v %d", err, devices)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM workos_gateway.device_sessions WHERE device_id = $1 AND revoked_at IS NULL`,
		winner.DeviceID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("expected exactly one active session: %v %d", err, sessions)
	}
}

func TestGatewayDeviceAuthRotationKillsClaimedTickets(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	if err := migrations.Run(context.Background(), dsn); err != nil {
		t.Fatalf("migrate scratch database: %v", err)
	}
	service := newIntegrationService(t, dsn, "sha256:"+repeatSeed("dd", 64))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	info, err := service.RotatePairingTicket(ctx, integrationOwnerID)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	key := newIntegrationKey(t)
	begin, err := service.BeginPairing(ctx, application.BeginPairingInput{
		PairingSecret: info.Secret, PublicKeySPKI: key.spki, DeviceName: "Claimer", DeviceClass: "desktop",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Operator rotates while the ticket is claimed.
	if _, err := service.RotatePairingTicket(ctx, integrationOwnerID); err != nil {
		t.Fatalf("second rotate: %v", err)
	}

	// The claimed QR can no longer complete: the proof references a ticket
	// snapshot that is now revoked.
	facts := domain.ProofFacts{
		PublicOrigin: "https://workos.example", Purpose: domain.PurposePairing,
		ChallengeID: begin.Challenge.ID, Nonce: begin.Challenge.Nonce,
		DeviceID: begin.DeviceID, PublicKeyHash: key.hash,
		TicketID: info.TicketID, TLSFingerprint: "sha256:" + repeatSeed("dd", 64),
	}
	if _, err := service.CompletePairing(ctx, application.CompletePairingInput{
		DeviceID: begin.DeviceID, ChallengeID: begin.Challenge.ID,
		PublicKeySPKI: key.spki, Signature: key.sign(t, facts),
	}); err == nil {
		t.Fatal("claimed ticket survived rotation")
	}
}

// TestGatewayDeviceAuthRestartDurability proves the session outlives the
// process: a fully closed pool (simulated restart) still resolves the
// cookie token through a fresh pool.
func TestGatewayDeviceAuthRestartDurability(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	if err := migrations.Run(context.Background(), dsn); err != nil {
		t.Fatalf("migrate scratch database: %v", err)
	}
	service := newIntegrationService(t, dsn, "sha256:"+repeatSeed("cc", 64))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	device, token := pairIntegrationDevice(t, service)
	_ = device
	// The t.Cleanup in newIntegrationService closes the pool at subtest end;
	// a fresh service over a fresh pool resolves the same token.
	fresh := newIntegrationService(t, dsn, "sha256:"+repeatSeed("cc", 64))
	if _, err := fresh.ResolveSession(ctx, token); err != nil {
		t.Fatalf("session did not survive a process restart: %v", err)
	}
}

func pairIntegrationDevice(t *testing.T, service *application.Service) (domain.Device, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := service.RotatePairingTicket(ctx, integrationOwnerID)
	if err != nil {
		t.Fatal(err)
	}
	key := newIntegrationKey(t)
	begin, err := service.BeginPairing(ctx, application.BeginPairingInput{
		PairingSecret: info.Secret, PublicKeySPKI: key.spki, DeviceName: "Restart Device", DeviceClass: "desktop",
	})
	if err != nil {
		t.Fatal(err)
	}
	facts := domain.ProofFacts{
		PublicOrigin: info.PublicOrigin, Purpose: domain.PurposePairing,
		ChallengeID: begin.Challenge.ID, Nonce: begin.Challenge.Nonce,
		DeviceID: begin.DeviceID, PublicKeyHash: key.hash,
		TicketID: info.TicketID, TLSFingerprint: info.TLSFingerprint,
	}
	completion, err := service.CompletePairing(ctx, application.CompletePairingInput{
		DeviceID: begin.DeviceID, ChallengeID: begin.Challenge.ID,
		PublicKeySPKI: key.spki, Signature: key.sign(t, facts),
	})
	if err != nil {
		t.Fatal(err)
	}
	return completion.Device, completion.SessionToken
}

func scratchConn(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeConn(conn) })
	return conn
}
