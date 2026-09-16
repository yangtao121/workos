// Package ports declares the artifact store's neutral boundaries (ADR-0033):
// metadata persistence and the on-disk bundle repository. Both are owned by
// runtime-host only.
package ports

import (
	"context"
	"io"

	"github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
)

// MetadataStore persists provenance separately from content-addressed bytes.
// A build task or owner-scoped import key is the concurrency arbiter.
type MetadataStore interface {
	// InsertReady stores the supplied verified metadata state: preparing for
	// a build awaiting its durable success verdict, ready for an admin import.
	// A duplicate task/import key returns its original row for replay checks.
	InsertReady(ctx context.Context, artifact domain.Artifact) (stored domain.Artifact, inserted bool, err error)
	GetByID(ctx context.Context, id string) (domain.Artifact, error)
	GetByOwnerDigest(ctx context.Context, owner, digest string) (domain.Artifact, error)
	GetByTask(ctx context.Context, taskID string) (domain.Artifact, error)
	GetByImportKey(ctx context.Context, owner, key string) (domain.Artifact, error)
	MarkState(ctx context.Context, id, state string) error
	OwnerUsageBytes(ctx context.Context, owner string) (int64, error)
	ListByState(ctx context.Context, state string) ([]domain.Artifact, error)
}

// BundleFiles is the runtime-owned on-disk repository: 0700 root, per-owner
// directories, content addressed by digest, atomic durable writes.
type BundleFiles interface {
	LockOwner(context.Context, string) (func(), error)
	UsageBytes(string) (int64, error)
	// TempFile returns a staging file inside the owner area. The caller
	// writes and seeks; PromoteFile makes it durable.
	TempFile(owner string) (StagingFile, error)
	// PromoteFile atomically moves a fully verified staging file to its
	// content-addressed location (tmp -> fsync -> rename -> dir fsync).
	PromoteFile(owner, digest string, staging StagingFile) error
	// OpenVerified re-hashes the stored bundle bytes and returns the path.
	// Digest mismatch or a missing file is a hard error, never a fallback.
	OpenVerified(owner, digest string) (path string, size int64, err error)
	// Has reports whether the content file exists for owner+digest.
	Has(owner, digest string) bool
	// CleanTemp removes every staging file. Called once at startup before
	// the admin socket serves, so no concurrent writer exists.
	CleanTemp() error
}

// StagingFile is one in-progress bundle write.
type StagingFile interface {
	io.Writer
	io.Seeker
	Name() string
	Finish() (size int64, err error) // sync + close, keeping the file
	Discard()                        // close + remove
}
