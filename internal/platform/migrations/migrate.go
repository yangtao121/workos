package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed files/*.sql
var migrationFiles embed.FS

const advisoryLockID int64 = 839367267011

// Run applies immutable, forward-only migrations under a PostgreSQL advisory lock.
func Run(ctx context.Context, databaseURL string) error {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect for migrations: %w", err)
	}
	defer conn.Close(ctx) //nolint:errcheck

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockID) //nolint:errcheck

	if _, err := conn.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS workos_meta;
		CREATE TABLE IF NOT EXISTS workos_meta.schema_migrations (
			name text PRIMARY KEY,
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL
		)`); err != nil {
		return fmt.Errorf("create migration metadata: %w", err)
	}

	entries, err := fs.ReadDir(migrationFiles, "files")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, readErr := migrationFiles.ReadFile("files/" + entry.Name())
		if readErr != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), readErr)
		}
		digest := sha256.Sum256(body)
		checksum := hex.EncodeToString(digest[:])

		var existing string
		err = conn.QueryRow(ctx, `SELECT checksum FROM workos_meta.schema_migrations WHERE name=$1`, entry.Name()).Scan(&existing)
		switch {
		case err == nil && existing != checksum:
			return fmt.Errorf("migration %s checksum changed", entry.Name())
		case err == nil:
			continue
		case err != pgx.ErrNoRows:
			return fmt.Errorf("inspect migration %s: %w", entry.Name(), err)
		}

		tx, beginErr := conn.Begin(ctx)
		if beginErr != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), beginErr)
		}
		if _, execErr := tx.Exec(ctx, string(body)); execErr != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return fmt.Errorf("apply migration %s: %w", entry.Name(), execErr)
		}
		if _, execErr := tx.Exec(ctx,
			`INSERT INTO workos_meta.schema_migrations(name, checksum, applied_at) VALUES ($1,$2,$3)`,
			entry.Name(), checksum, time.Now().UTC()); execErr != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return fmt.Errorf("record migration %s: %w", entry.Name(), execErr)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), commitErr)
		}
	}

	return nil
}
