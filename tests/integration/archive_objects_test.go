//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	indexerapp "github.com/yangtao121/workos/internal/indexer/application"
	indexerdomain "github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// TestArchiveObjects proves the ADR-0017 §5 generic archive minimal slice:
// content-addressed puts with dedup, the 8 MiB bound, media-type grammar,
// the per-owner object count bound, and the guarantee that archive objects
// never enter the search projection.
func TestArchiveObjects(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	generator := ids.UUIDv7{}
	projection, err := newModelProjection(pool, generator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projection.EnsureBootstrapGeneration(ctx, timeNowUTC()); err != nil {
		t.Fatalf("bootstrap generation: %v", err)
	}
	archive, err := indexerapp.NewArchiveService(projection)
	if err != nil {
		t.Fatal(err)
	}
	search := indexerapp.NewSearchServiceForTest(projection)
	owner := "01999999-9999-7999-8999-000000001001"

	payload := []byte("archive payload one — opaque bytes, never searchable")
	put, inserted, err := archive.Put(ctx, owner, "application/octet-stream", payload)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !inserted || put.ByteCount != int64(len(payload)) {
		t.Fatalf("first put drifted: inserted=%v bytes=%d", inserted, put.ByteCount)
	}

	// Content-addressed dedup: the same bytes return the same object id.
	dup, inserted, err := archive.Put(ctx, owner, "text/plain", payload)
	if err != nil {
		t.Fatalf("dedup put: %v", err)
	}
	if inserted || dup.ID != put.ID {
		t.Fatalf("dedup drifted: id=%s vs %s inserted=%v", dup.ID, put.ID, inserted)
	}

	// Get revalidates the stored byte count against the bytes.
	object, content, err := archive.Get(ctx, owner, put.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(content, payload) || object.Sha256 != put.Sha256 {
		t.Fatal("get content or digest drifted")
	}

	// Bound enforcement: oversize and grammar violations fail closed.
	if _, _, err := archive.Put(ctx, owner, "application/octet-stream", make([]byte, indexerdomain.ArchiveMaxObjectBytes+1)); err == nil {
		t.Fatal("oversize object must be rejected")
	}
	if _, _, err := archive.Put(ctx, owner, "bad media type", payload); err == nil {
		t.Fatal("media type with spaces must be rejected")
	}
	if _, _, err := archive.Put(ctx, "not-a-uuid", "application/octet-stream", payload); err == nil {
		t.Fatal("invalid owner must be rejected")
	}

	// Per-owner object count bound: fill to the limit (the first put counts
	// as object one), then one more fails.
	for i := 1; i < indexerdomain.ArchiveMaxObjects; i++ {
		body := []byte("archive object " + itoa(i))
		if _, _, err := archive.Put(ctx, owner, "application/octet-stream", body); err != nil {
			t.Fatalf("fill put %d: %v", i, err)
		}
	}
	if _, _, err := archive.Put(ctx, owner, "application/octet-stream", []byte("one object too many")); err == nil {
		t.Fatal("per-owner bound must reject further puts")
	}
	objects, err := archive.List(ctx, owner, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 200 {
		t.Fatalf("list page = %d, want 200", len(objects))
	}

	// Archive objects never enter the search projection.
	if _, err := projection.EnsureBootstrapGeneration(ctx, timeNowUTC()); err != nil {
		t.Fatal(err)
	}
	page, err := search.SearchHybrid(ctx, indexerapp.SearchInput{
		OwnerUserID: owner,
		ProjectID:   "01999999-9999-7999-8999-000000001002",
		RawQuery:    "archive payload",
		PageSize:    20,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Page.Hits) != 0 {
		t.Fatalf("archive objects leaked into search: %d hits", len(page.Page.Hits))
	}
}

func timeNowUTC() time.Time { return time.Now().UTC() }

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
