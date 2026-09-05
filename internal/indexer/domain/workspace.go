// Workspace file source facts (ADR-0017 §4): the bounded grammar every
// mounted file must satisfy before it becomes a document, and the mount
// lifecycle vocabulary. Pure validation — no filesystem, no store.
package domain

import (
	"crypto/sha256"
	"errors"
	"path"
	"strings"

	"github.com/google/uuid"
)

var (
	ErrWorkspaceInvalidPath = errors.New("workspace file path is invalid")
	// ErrWorkspaceDegraded reports a mount-level failure recorded on the
	// source instead of a silent stop.
	ErrWorkspaceDegraded = errors.New("workspace mount is degraded")
)

// Bounded ingestion budget per sync pass (ADR-0017 §4). The per-file cap is
// the stricter durable content bound (512 KiB documents CHECK), which the
// 1 MiB walk budget never reaches.
const (
	WorkspaceMaxFiles      = 1000
	WorkspaceMaxFileBytes  = 512 * 1024
	WorkspaceMaxRootLength = 4096
)

// WorkspaceSkip reasons are sanitized, bounded categories — never paths or
// content from the mount.
const (
	SkipIgnored   = "ignored"
	SkipOversize  = "oversize"
	SkipBinary    = "binary"
	SkipExtension = "extension"
	SkipSymlink   = "symlink"
	SkipLimit     = "limit"
	SkipInvalid   = "invalid"
)

// WorkspaceSourceStatus is the durable mount lifecycle.
const (
	WorkspaceActive   = "active"
	WorkspaceDegraded = "degraded"
	WorkspaceStopped  = "stopped"
)

// DegradedReason values are fixed categories recorded on the source.
const (
	DegradedMountMissing = "mount-missing"
	DegradedMountNotDir  = "mount-not-dir"
)

// WorkspaceExtensions is the allowlist of text file extensions ingested as
// workspace documents.
var WorkspaceExtensions = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".go": true,
	".ts": true, ".tsx": true, ".js": true, ".json": true,
	".sql": true, ".yaml": true, ".yml": true,
}

// workspaceIgnoredNames are never traversed (ADR-0017 §4 ignore rules).
var workspaceIgnoredNames = map[string]bool{
	".git": true, "node_modules": true, ".env": true,
}

// ValidWorkspaceSourceStatus pins the mount lifecycle grammar.
func ValidWorkspaceSourceStatus(status string) bool {
	return status == WorkspaceActive || status == WorkspaceDegraded || status == WorkspaceStopped
}

// ValidWorkspaceRoot pins the mount root grammar: absolute, bounded, no
// traversal, never a bare relative guess.
func ValidWorkspaceRoot(root string) bool {
	if root == "" || len(root) > WorkspaceMaxRootLength || !path.IsAbs(root) {
		return false
	}
	clean := path.Clean(root)
	return clean == root && !strings.Contains(root, "\x00")
}

// WorkspaceRelPath validates one walked file path relative to the mount
// root and returns the document title: the relative path itself, bounded by
// the stored title grammar. Ignore rules are a separate verdict so the
// walker can record skipped reasons honestly.
func WorkspaceRelPath(relPath string) (string, error) {
	if relPath == "" || relPath == "." {
		return "", ErrWorkspaceInvalidPath
	}
	clean := path.Clean(relPath)
	if clean != relPath || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "..") {
		return "", ErrWorkspaceInvalidPath
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return "", ErrWorkspaceInvalidPath
		}
	}
	title := clean
	if len(title) > 200 {
		// Stored titles are bounded at 200; overflow keeps a stable suffix
		// so long paths still index deterministically.
		title = "…" + title[len(title)-199:]
	}
	return title, nil
}

// WorkspaceIgnoredName reports whether a path component is ignored by the
// bounded ingestion rules (ADR-0017 §4).
func WorkspaceIgnoredName(name string) bool { return workspaceIgnoredNames[name] }

// WorkspaceExtension reports whether a walked file passes the extension
// allowlist.
func WorkspaceExtension(relPath string) bool {
	return WorkspaceExtensions[strings.ToLower(path.Ext(relPath))]
}

// LooksBinary rejects files whose first bytes contain a NUL — the cheap,
// deterministic binary sniff for the bounded ingestion path.
func LooksBinary(content []byte) bool {
	limit := len(content)
	if limit > 8000 {
		limit = 8000
	}
	for i := 0; i < limit; i++ {
		if content[i] == 0 {
			return true
		}
	}
	return false
}

// WorkspaceSourceID derives the deterministic per-scope document identity of
// one relative path: same path, same document row, so re-syncs upsert instead
// of duplicating. The v7 grammar (version 7, RFC 4122 variant) is fixed and
// the hash fills the random payload with a zero epoch — deterministic by
// construction while every ID stays a canonical UUIDv7.
func WorkspaceSourceID(ownerUserID, projectID, relPath string) string {
	sum := sha256.Sum256([]byte("workos.indexer.workspace.source.v1\n" +
		ownerUserID + "\x00" + projectID + "\x00" + relPath))
	var id uuid.UUID
	copy(id[:], sum[:16])
	id[6] = (id[6] & 0x0f) | 0x70
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

// Generic archive bounds (ADR-0017 §5).
const (
	ArchiveMaxObjectBytes = 8 * 1024 * 1024
	// Local-first single-owner scale; the count is enforced by the service
	// before every put.
	ArchiveMaxObjects   = 200
	ArchiveMaxMediaType = 128
)

// ValidArchiveMediaType pins the bounded media-type grammar: printable
// ASCII, no spaces beyond the type/subtype+parameter shape, bounded length.
func ValidArchiveMediaType(mediaType string) bool {
	if mediaType == "" || len(mediaType) > ArchiveMaxMediaType {
		return false
	}
	for _, r := range mediaType {
		if r <= 0x20 || r >= 0x7f {
			return false
		}
	}
	return true
}
