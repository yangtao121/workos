// Workspace file source facts (ADR-0017 §4): the bounded grammar every
// mounted file must satisfy before it becomes a document, and the mount
// lifecycle vocabulary. Pure validation — no filesystem, no store.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrWorkspaceRebuildLimit  = errors.New("workspace rebuild copy limit exceeded")
	ErrWorkspaceConflict      = errors.New("workspace source changed during scan")
	ErrWorkspaceSymlinkEscape = errors.New("workspace root resolves through a symlink")
	ErrWorkspaceInvalidPath   = errors.New("workspace file path is invalid")
	// ErrWorkspaceDegraded reports a mount-level failure recorded on the
	// source instead of a silent stop.
	ErrWorkspaceDegraded = errors.New("workspace mount is degraded")
)

// Bounded ingestion budgets per complete sync pass.
const (
	WorkspaceMaxFiles            = 1000
	WorkspaceMaxFileBytes        = 512 * 1024
	WorkspaceMaxTotalBytes       = 16 * 1024 * 1024
	WorkspaceMaxEntries          = 10000
	WorkspaceMaxDepth            = 16
	WorkspaceMaxPathBytes        = 1024
	WorkspaceMaxRootLength       = 4096
	WorkspaceMaxRebuildDocuments = 2000
	WorkspaceMaxRebuildBytes     = 64 * 1024 * 1024
)

// WorkspaceFailure contains only a fixed category, never a filesystem error.
type WorkspaceFailure struct{ Reason string }

func (e *WorkspaceFailure) Error() string { return "workspace mount is degraded: " + e.Reason }
func (e *WorkspaceFailure) Unwrap() error { return ErrWorkspaceDegraded }

// WorkspaceSkip reasons are sanitized, bounded categories — never paths or
// content from the mount.
const (
	SkipIgnored   = "ignored"
	SkipOversize  = "oversize"
	SkipBinary    = "binary"
	SkipExtension = "extension"
	SkipSymlink   = "symlink"
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
	DegradedMountUnsafe  = "mount-unsafe"
	DegradedReadFailed   = "read-failed"
	DegradedScanLimit    = "scan-limit"
	DegradedUnavailable  = "filesystem-unavailable"
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
	if root == "" || root == "/" || !utf8.ValidString(root) || len(root) > WorkspaceMaxRootLength || !path.IsAbs(root) {
		return false
	}
	clean := path.Clean(root)
	return clean == root && !strings.ContainsFunc(root, unicode.IsControl)
}

// WorkspaceRelPath validates one walked file path relative to the mount
// root and returns the document title: the relative path itself, bounded by
// the stored title grammar. Ignore rules are a separate verdict so the
// walker can record skipped reasons honestly.
func WorkspaceRelPath(relPath string) (string, error) {
	if relPath == "" || relPath == "." || len(relPath) > WorkspaceMaxPathBytes ||
		!utf8.ValidString(relPath) || strings.ContainsAny(relPath, "\\") || strings.ContainsFunc(relPath, unicode.IsControl) ||
		strings.Count(relPath, "/") >= WorkspaceMaxDepth {
		return "", ErrWorkspaceInvalidPath
	}
	clean := path.Clean(relPath)
	if clean != relPath || strings.HasPrefix(clean, "/") || (clean == ".." || strings.HasPrefix(clean, "../")) {
		return "", ErrWorkspaceInvalidPath
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 255 {
			return "", ErrWorkspaceInvalidPath
		}
	}
	title := clean
	if runes := []rune(title); len(runes) > 200 {
		title = "…" + string(runes[len(runes)-199:])
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

// LooksBinary excludes invalid UTF-8 and disallowed controls anywhere in a bounded text file.
func LooksBinary(content []byte) bool {
	return !utf8.Valid(content) || hasControl(string(content))
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

// WorkspaceETag identifies the exact operator binding and its durable version.
func WorkspaceETag(id string, updated time.Time) string {
	sum := sha256.Sum256([]byte(id + "\n" + updated.UTC().Format(time.RFC3339Nano)))
	return "sha256:" + hex.EncodeToString(sum[:])
}
