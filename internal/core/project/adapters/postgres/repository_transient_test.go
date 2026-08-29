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

// Transient-outage coverage for the base Project repository paths. The pool
// points at 127.0.0.1:1 — a port the kernel refuses synchronously — so pgx
// produces a real *pgconn.ConnectError (wrapping the refused *net.OpError
// dial) on the first acquisition. Every base storage path must surface that
// failure wrapped with ports.ErrStoreUnavailable so transports can answer a
// sanitized Unavailable; nothing here injects a sentinel, the driver's own
// error values must carry the verdict end to end. The sentinel wrapping
// itself is the shared storeError in store.go, the exact classifier the
// installation command paths use.

func TestBaseRepositoryTransientFailuresCarryStoreUnavailable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, refusedPoolDSN)
	if err != nil {
		t.Fatalf("pgxpool.New parses the config without dialing: %v", err)
	}
	t.Cleanup(pool.Close)
	repository := New(pool)

	ownerUserID := "01999999-9999-7999-8999-999999999901"
	projectID := "01999999-9999-7999-8999-999999999902"
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	command := ports.CreateCommand{
		Project: domain.Project{
			ID: projectID, OwnerUserID: ownerUserID, Name: "Outage Probe",
			KnowledgeCollectionID: "01999999-9999-7999-8999-999999999903",
			ArtifactCollectionID:  "01999999-9999-7999-8999-999999999904",
			Revision:              1, CreatedAt: now, UpdatedAt: now,
		},
		IdempotencyKey: "outage-probe-key",
		RequestDigest:  domain.CreateRequestDigest("Outage Probe", "", nil, nil),
		Now:            now,
	}

	t.Run("LookupCreateRequest fails", func(t *testing.T) {
		_, _, err := repository.LookupCreateRequest(ctx, ownerUserID, "key")
		assertUnavailableFromRealDialFailure(t, err)
	})
	t.Run("CreateProject begin fails", func(t *testing.T) {
		_, err := repository.CreateProject(ctx, command)
		assertUnavailableFromRealDialFailure(t, err)
	})
	t.Run("GetProject fails", func(t *testing.T) {
		_, err := repository.GetProject(ctx, ownerUserID, projectID)
		assertUnavailableFromRealDialFailure(t, err)
	})
	t.Run("ListProjects fails", func(t *testing.T) {
		_, err := repository.ListProjects(ctx, ownerUserID, "", 51, false)
		assertUnavailableFromRealDialFailure(t, err)
	})
	t.Run("UpdateProject begin fails", func(t *testing.T) {
		_, err := repository.UpdateProject(ctx, command.Project, 1)
		assertUnavailableFromRealDialFailure(t, err)
	})
	t.Run("ArchiveProject begin fails", func(t *testing.T) {
		_, err := repository.ArchiveProject(ctx, ownerUserID, projectID, 1)
		assertUnavailableFromRealDialFailure(t, err)
	})
}

// assertUnavailableFromRealDialFailure proves the repository wrapped a
// genuine pgx dial failure — *pgconn.ConnectError over a refused *net.OpError
// dial — with the store-unavailable sentinel.
func assertUnavailableFromRealDialFailure(t *testing.T, err error) {
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
