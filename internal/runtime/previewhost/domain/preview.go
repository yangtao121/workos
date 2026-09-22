// Package domain holds the workspace dev preview invariants (ADR-0030 B08):
// the fixed state grammar, the bounded 30-minute serving ceiling, and the
// untrusted identifier grammars. Domain never imports database, Connect,
// HTTP, or other modules' adapters.
package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// The fixed preview state grammar of the wire contract. "unavailable" is a
// wire-only honest verdict (for example a runtime host that cannot serve the
// workspace); it is never a stored state.
const (
	StateRunning     = "running"
	StateQueued      = "queued"
	StateFailed      = "failed"
	StateStopped     = "stopped"
	StateExpired     = "expired"
	StateUnavailable = "unavailable"
)

// PreviewTTL is the bounded serving ceiling of one preview: the same
// 30-minute session ceiling the PTY and native runner domains enforce
// (their domain.SessionTTL). ADR-0037 adds an explicit manual-stop mode;
// legacy requests keep this bounded duration.
const PreviewTTL = 30 * time.Minute

var (
	// ErrInvalid marks a request that violates the preview grammar.
	ErrConflict = errors.New("preview intent conflicts with recorded request")
	ErrInvalid  = errors.New("workspace preview request is invalid")
	// ErrNotFound marks an unknown, foreign, stopped, or expired preview —
	// deliberately indistinguishable at the transport boundary.
	ErrNotFound = errors.New("workspace preview is not available for this owner")
	// ErrNoWorkspace marks a Start for a project without an operator-
	// registered workspace binding on this runtime host. The transport maps
	// it to the honest FailedPrecondition verdict.
	ErrNoWorkspace = errors.New("no workspace registered for this owner and project")
	// ErrUnavailable marks a temporarily unreachable preview store.
	ErrUnavailable = errors.New("workspace preview store is temporarily unavailable")
	// ErrFileLimit marks a serving read whose file exceeds the bounded
	// preview file cap; the transport answers a fixed 403.
	ErrFileLimit = errors.New("workspace preview file limit exceeded")
)

// MaxPreviewFileBytes caps one served preview file (8 MiB), bounding memory
// and the response body. Larger files fail closed; the bound must never be
// relaxed to unbounded values.
const MaxPreviewFileBytes = 8 << 20

// MaxIdempotencyKeyRunes bounds the start idempotency key: valid UTF-8, no
// C0/C1/NUL control characters, never trimmed.
const MaxIdempotencyKeyRunes = 128

// ValidIdempotencyKey enforces the command key grammar.
func ValidIdempotencyKey(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
		count++
		if count > MaxIdempotencyKeyRunes {
			return false
		}
	}
	return count > 0
}

// ValidPreviewUUID accepts only the canonical lowercase hyphenated UUIDv7
// grammar the server generates, so non-v7, uppercase, or wrong-variant
// identifiers are invalid before storage is consulted. This mirrors the
// canonical grammar of the interactive session domains; it is duplicated
// here because cross-module domain imports are forbidden.
func ValidPreviewUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, c := range []byte(value) {
		switch index {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	if value[14] != '7' {
		return false
	}
	switch value[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
}

// MaxPreviewAssetPathBytes bounds one serving path.
const MaxPreviewAssetPathBytes = 1024

// MaxPreviewAssetSegments bounds the path depth of one serving request.
const MaxPreviewAssetSegments = 8

// ValidPreviewAssetPath accepts only safe relative POSIX-ish request paths:
// non-empty, bounded, no backslash or colon, at most eight slash-separated
// segments, and no segment starting with a dot — which excludes ".", "..",
// every dotfile, and ".git" before any filesystem access. This mirrors the
// workspace file grammar of the surface module (recorded duplication for the
// module boundary).
func ValidPreviewAssetPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || len(value) > MaxPreviewAssetPathBytes ||
		strings.ContainsAny(value, "\\:") {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) > MaxPreviewAssetSegments {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.HasPrefix(part, ".") || len(part) > 255 {
			return false
		}
		for _, r := range part {
			if r < 32 || (r >= 127 && r <= 159) {
				return false
			}
		}
	}
	return true
}
