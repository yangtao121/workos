package postgres

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
)

// Transient-outage coverage for the installation repository. The pool points
// at 127.0.0.1:1 — a port the kernel refuses synchronously — so pgx produces
// a real *pgconn.ConnectError (wrapping the refused *net.OpError dial) on the
// first acquisition. Every repository storage path must surface that failure
// wrapped with ports.ErrStoreUnavailable so transports can answer a sanitized
// Unavailable; nothing here injects a sentinel, the driver's own error values
// must carry the verdict end to end.

// refusedPoolDSN targets a port that refuses connections immediately.
// connect_timeout=1 bounds any slow dial path and pool_max_conns=1 keeps the
// single acquisition slot deterministic. pgxpool.New only parses this string;
// it never dials.
const refusedPoolDSN = "postgres://workos:workos@127.0.0.1:1/workos?sslmode=disable&connect_timeout=1&pool_max_conns=1"

func TestRepositoryTransientFailuresCarryStoreUnavailable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, refusedPoolDSN)
	if err != nil {
		t.Fatalf("pgxpool.New parses the config without dialing: %v", err)
	}
	t.Cleanup(pool.Close)
	repository := New(pool)

	// Domain-legal command shapes: the UUIDs follow the repo's fixed UUIDv7
	// test convention, the pinned app matches invariantFixture, and the
	// digests are computed by the same domain helpers the application uses.
	ownerUserID := "01999999-9999-7999-8999-999999999901"
	projectID := "01999999-9999-7999-8999-999999999902"
	installationID := "01999999-9999-7999-8999-999999999903"
	idempotencyKey := "install-board-app-20260829"
	pinned := domain.PinnedApp{
		AppID: "board-app", Version: "1.2.0",
		ManifestDigest: "sha256:" + repeatChar('a', 64), Scope: "user",
		Permissions: []string{"agent.event.watch", "agent.task.run"},
	}
	grant := []string{"agent.task.run"}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	installDigest := domain.InstallationRequestDigestWithGrants("install", projectID, pinned.AppID, pinned.Version, "", 1, grant)
	setGrantsDigest := domain.SetGrantsRequestDigest(projectID, installationID, 1, grant)

	t.Run("LookupInstallationRequest query fails", func(t *testing.T) {
		_, _, err := repository.LookupInstallationRequest(ctx, ownerUserID, idempotencyKey)
		assertStoreUnavailableWithRealConnectError(t, err)
	})
	t.Run("Install begin fails", func(t *testing.T) {
		_, err := repository.Install(ctx, ports.InstallCommand{
			OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey,
			ProjectID: projectID, AppID: pinned.AppID, Pinned: pinned,
			GrantedPermissions: grant, ExpectedRevision: 1, RequestDigest: installDigest,
			NewInstallationID: installationID, Now: now,
		})
		assertStoreUnavailableWithRealConnectError(t, err)
	})
	t.Run("SetAppGrants begin fails", func(t *testing.T) {
		_, err := repository.SetAppGrants(ctx, ports.SetAppGrantsCommand{
			OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey,
			ProjectID: projectID, InstallationID: installationID, Pinned: pinned,
			GrantedPermissions: grant, ExpectedRevision: 1, RequestDigest: setGrantsDigest, Now: now,
		})
		assertStoreUnavailableWithRealConnectError(t, err)
	})
}

// assertStoreUnavailableWithRealConnectError proves the repository wrapped a
// genuine pgx dial failure — *pgconn.ConnectError over a refused *net.OpError
// dial — with the store-unavailable sentinel, not an injected fake.
func assertStoreUnavailableWithRealConnectError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a storage failure, got nil")
	}
	if !errors.Is(err, ports.ErrStoreUnavailable) {
		t.Fatalf("refused database must carry ports.ErrStoreUnavailable, got: %v", err)
	}
	var connectErr *pgconn.ConnectError
	if !errors.As(err, &connectErr) {
		t.Fatalf("cause must be a real *pgconn.ConnectError, got %T: %v", err, err)
	}
	var opErr *net.OpError
	if !errors.As(connectErr, &opErr) || opErr.Op != "dial" {
		t.Fatalf("connect error must wrap the refused dial (*net.OpError), got: %v", connectErr)
	}
}

// TestStoreErrorSentinelMatrix pins storeError's port-boundary contract on
// real driver error values: transient failures (operator shutdown,
// connection exception, caller deadline, refused dial) gain
// ports.ErrStoreUnavailable, while integrity violations stay opaque
// internals. The underlying SQLSTATE/Go-type classification itself is pinned
// in internal/platform/dbtransient (TestIsTransientClassificationMatrix);
// this matrix pins only the adapter's sentinel wrapping and cause
// preservation.
func TestStoreErrorSentinelMatrix(t *testing.T) {
	t.Parallel()
	// A real refused dial, the net error shape pgx surfaces through
	// *pgconn.ConnectError when the database process is unreachable.
	_, dialErr := net.DialTimeout("tcp", "127.0.0.1:1", 50*time.Millisecond)
	if dialErr == nil {
		t.Fatal("127.0.0.1:1 unexpectedly accepted a connection")
	}
	cases := []struct {
		name        string
		err         error
		unavailable bool
	}{
		{name: "operator shutdown 57P01 is transient", err: &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"}, unavailable: true},
		{name: "connection failure 08006 is transient", err: &pgconn.PgError{Code: "08006", Message: "connection failure"}, unavailable: true},
		{name: "unique violation 23505 stays internal", err: &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}, unavailable: false},
		{name: "caller deadline exceeded is transient", err: context.DeadlineExceeded, unavailable: true},
		{name: "real refused dial is transient", err: dialErr, unavailable: true},
	}
	for _, tc := range cases {
		wrapped := storeError("operation", tc.err)
		if got := errors.Is(wrapped, ports.ErrStoreUnavailable); got != tc.unavailable {
			t.Errorf("%s: ErrStoreUnavailable presence = %v, want %v (%v)", tc.name, got, tc.unavailable, wrapped)
		}
		if !errors.Is(wrapped, tc.err) {
			t.Errorf("%s: wrapped error must preserve the original cause: %v", tc.name, wrapped)
		}
	}
	if storeError("operation", nil) != nil {
		t.Error("nil must stay nil")
	}
}
