// Package domain holds the Artifact module's invariants: the immutable
// web bundle artifact fact, its canonical digest, and the request-boundary
// grammar. Domain never imports database, Connect, HTTP, or other modules'
// adapters.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	// ErrInvalid marks a request that violates the artifact grammar.
	ErrInvalid = errors.New("artifact request is invalid")
	// ErrNotFound marks an unknown or foreign artifact, read, or asset.
	ErrNotFound = errors.New("artifact is not available for this owner")
	// ErrIdempotencyConflict marks a create key consumed by a different
	// canonical request.
	ErrIdempotencyConflict = errors.New("artifact idempotency key was used for a different request")
	// ErrUnsupported marks artifact payloads other than the implemented
	// web bundle subtype.
	ErrUnsupported = errors.New("artifact type is not supported")
	// ErrDigestMismatch marks a referenced artifact whose digest does not
	// match the exact expected digest. Callers map it per context: public
	// registration sees InvalidArgument, resolution fails closed internally.
	ErrDigestMismatch = errors.New("artifact digest does not match the reference")
)

// The single implemented artifact subtype. Artifact storage beyond this
// subtype remains unimplemented and must keep reporting so honestly.
const (
	TypeWebBundle   = "app.web-bundle.v1"
	MediaTypeBundle = "application/vnd.workos.web-bundle.v1"

	// bundleDigestVersion prefixes the canonical bundle digest encoding so a
	// future bundle format can never collide with v1 digests.
	bundleDigestVersion = "workos.web-bundle.v1"
)

// Bundle upload limits. They bound decompression, memory, and row sizes; the
// constants must never be relaxed to unbounded values.
const (
	MaxBundleFiles      = 128
	MaxBundleFileBytes  = 512 * 1024
	MaxBundleTotalBytes = 2 * 1024 * 1024
	MaxBundlePathBytes  = 240
)

// Artifact is the immutable owner-scoped metadata fact shared by both
// implemented subtypes. ContentRef is an opaque server-generated reference
// that never exposes a filesystem path; review artifacts carry no external
// content reference and bind ProjectID/SourceTaskID instead, while web
// bundles leave both empty.
type Artifact struct {
	ID             string
	OwnerUserID    string
	Type           string
	Title          string
	MediaType      string
	ContentRef     string
	Digest         string
	Entrypoint     string
	FileCount      int
	TotalSizeBytes int64
	CreatedAt      time.Time
	ProjectID      string
	SourceTaskID   string
}

// BundleFile is one normalized bundle file: a safe relative POSIX path, the
// server-derived media type, and the exact bytes. FileDigest covers the file
// bytes alone and serves as the asset etag.
type BundleFile struct {
	Path       string
	MediaType  string
	Content    []byte
	SizeBytes  int
	FileDigest string
}

// WebBundle is the normalized, validated bundle: files sorted by path, the
// entrypoint among them, and the canonical order-independent digest.
type WebBundle struct {
	Entrypoint string
	Files      []BundleFile
}

// BundleFileInput is one untrusted file as submitted.
type BundleFileInput struct {
	Path    string
	Content []byte
}

// NormalizeWebBundle validates and normalizes the untrusted upload. The result
// is independent of the submission order: files are sorted by path, duplicate
// and case-folded-colliding paths are rejected, and every media type is
// derived from the controlled extension table.
func NormalizeWebBundle(entrypoint string, files []BundleFileInput) (WebBundle, error) {
	if entrypoint == "" || len(entrypoint) > MaxBundlePathBytes {
		return WebBundle{}, ErrInvalid
	}
	if !validBundlePath(entrypoint) || !strings.HasSuffix(entrypoint, ".html") {
		return WebBundle{}, ErrInvalid
	}
	if len(files) == 0 || len(files) > MaxBundleFiles {
		return WebBundle{}, ErrInvalid
	}
	normalized := make([]BundleFile, 0, len(files))
	seen := make(map[string]string, len(files)) // exact path -> media type
	seenFolded := make(map[string]struct{}, len(files))
	total := 0
	for _, file := range files {
		if file.Path == "" || len(file.Path) > MaxBundlePathBytes || !validBundlePath(file.Path) {
			return WebBundle{}, ErrInvalid
		}
		if len(file.Content) == 0 || len(file.Content) > MaxBundleFileBytes {
			return WebBundle{}, ErrInvalid
		}
		mediaType, ok := MediaTypeForPath(file.Path)
		if !ok {
			return WebBundle{}, ErrInvalid
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return WebBundle{}, ErrInvalid
		}
		folded := strings.ToLower(file.Path)
		if _, collision := seenFolded[folded]; collision {
			return WebBundle{}, ErrInvalid
		}
		seen[file.Path] = mediaType
		seenFolded[folded] = struct{}{}
		total += len(file.Content)
		if total > MaxBundleTotalBytes {
			return WebBundle{}, ErrInvalid
		}
		sum := sha256.Sum256(file.Content)
		normalized = append(normalized, BundleFile{
			Path: file.Path, MediaType: mediaType, Content: file.Content,
			SizeBytes: len(file.Content), FileDigest: DigestPrefix + hex.EncodeToString(sum[:]),
		})
	}
	if _, ok := seen[entrypoint]; !ok {
		return WebBundle{}, ErrInvalid
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Path < normalized[j].Path })
	return WebBundle{Entrypoint: entrypoint, Files: normalized}, nil
}

// CanonicalDigest derives the order-independent bundle digest: a
// length-prefixed encoding of the version identifier, the entrypoint, and
// every path/content pair in sorted path order. Length prefixes make the
// encoding unambiguous; only this function may define the v1 format.
func (b WebBundle) CanonicalDigest() string {
	digest := sha256.New()
	writeField := func(value string) {
		digest.Write([]byte(fmt.Sprintf("%d:", len(value))))
		digest.Write([]byte(value))
	}
	writeField(bundleDigestVersion)
	writeField(b.Entrypoint)
	files := make([]BundleFile, len(b.Files))
	copy(files, b.Files)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, file := range files {
		writeField(file.Path)
		digest.Write([]byte(fmt.Sprintf("%d:", len(file.Content))))
		digest.Write(file.Content)
	}
	return DigestPrefix + hex.EncodeToString(digest.Sum(nil))
}

// Asset returns the one normalized file matching an already validated request
// path, or ErrNotFound when the bundle has no such file.
func (b WebBundle) Asset(path string) (BundleFile, error) {
	for _, file := range b.Files {
		if file.Path == path {
			return file, nil
		}
	}
	return BundleFile{}, ErrNotFound
}

// validBundlePath accepts only safe relative POSIX paths: ASCII segments that
// start alphanumeric and continue alphanumeric, dot, underscore, or hyphen,
// joined by single slashes. This excludes empty, absolute, and dot segments,
// backslashes, control characters, duplicate slashes, and — with the segment
// start rule — percent-encoding ambiguity and traversal fragments before any
// decoding happens.
func validBundlePath(path string) bool {
	if path == "" || len(path) > MaxBundlePathBytes {
		return false
	}
	if strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") || strings.Contains(path, "//") {
		return false
	}
	segmentStart := true
	for index := 0; index < len(path); index++ {
		c := path[index]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			segmentStart = false
		case c == '.' || c == '_' || c == '-':
			// A segment must start alphanumeric, so lone dot segments ('.',
			// '..') never pass; dots are only infix inside a segment.
			if segmentStart {
				return false
			}
		case c == '/':
			if segmentStart {
				return false
			}
			segmentStart = true
		default:
			return false
		}
	}
	return !segmentStart
}

// mediaTypes is the controlled server-side extension table. Unknown or
// server-executable types are rejected at upload; clients never pick a type.
var mediaTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".ico":   "image/x-icon",
	".txt":   "text/plain; charset=utf-8",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

// MediaTypeForPath reports the server-derived media type for an already
// normalized bundle path. It is the single source for stored and served types.
func MediaTypeForPath(path string) (string, bool) {
	mediaType, ok := mediaTypes[strings.ToLower(extension(path))]
	return mediaType, ok
}

// ValidBundleAssetPath reports whether path satisfies the normalized bundle
// path grammar and maps to a known media type. It guards read boundaries so
// traversal-shaped or unknown-type requests never reach storage.
func ValidBundleAssetPath(path string) bool {
	if !validBundlePath(path) {
		return false
	}
	_, ok := MediaTypeForPath(path)
	return ok
}

func extension(path string) string {
	if index := strings.LastIndexByte(path, '.'); index >= 0 {
		return path[index:]
	}
	return ""
}

// DigestPrefix is the canonical digest format shared with the registry and
// installation grammar: sha256 lowercase hex.
const DigestPrefix = "sha256:"

// ValidArtifactDigest matches the canonical digest shape.
func ValidArtifactDigest(value string) bool {
	if len(value) != len(DigestPrefix)+64 || !strings.HasPrefix(value, DigestPrefix) {
		return false
	}
	for index := len(DigestPrefix); index < len(value); index++ {
		c := value[index]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidArtifactUUID reports whether value is a canonical lowercase hyphenated
// UUIDv7: 8-4-4-4-12 hex with the version nibble fixed at 7 and an RFC 4122
// variant. Get/List boundaries accept only the canonical grammar the server
// generates, so non-v7, uppercase, or wrong-variant identifiers are invalid
// before storage is consulted.
func ValidArtifactUUID(value string) bool {
	return validArtifactUUIDv7(value)
}

func validArtifactUUIDv7(value string) bool {
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

// MaxArtifactIdempotencyKeyRunes bounds the create-command key: valid UTF-8,
// no C0/C1/NUL control characters, never trimmed.
const MaxArtifactIdempotencyKeyRunes = 128

// ValidArtifactIdempotencyKey enforces the command key grammar.
func ValidArtifactIdempotencyKey(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
		count++
		if count > MaxArtifactIdempotencyKeyRunes {
			return false
		}
	}
	return count > 0
}

// MaxArtifactTitleRunes bounds the human-facing title.
const MaxArtifactTitleRunes = 200

// ValidArtifactTitle enforces the title grammar: printable, trimmed-free,
// non-empty, bounded.
func ValidArtifactTitle(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
		count++
		if count > MaxArtifactTitleRunes {
			return false
		}
	}
	return true
}

// CreateRequestDigest digests the canonical create request: the title plus
// the already order-independent bundle canonical digest. Submission order
// never changes it; any metadata, entrypoint, path, or content change does.
func CreateRequestDigest(title, bundleDigest string) string {
	digest := sha256.New()
	digest.Write([]byte(fmt.Sprintf("workos.artifact-create.v1|t:%d:", len(title))))
	digest.Write([]byte(title))
	digest.Write([]byte(fmt.Sprintf("|d:%d:", len(bundleDigest))))
	digest.Write([]byte(bundleDigest))
	return DigestPrefix + hex.EncodeToString(digest.Sum(nil))
}
