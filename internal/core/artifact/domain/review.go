// Review artifact subtype facts: canonical bounded Markdown and unified-diff
// documents materialized by a project agent under an active task lease
// (ADR-0008). All entry points — private materialization, public read, store
// replay — share the grammar and digest implementations in this file; no
// transport, worker, or renderer may restate them.
package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"
	"unicode/utf8"
)

// The two canonical review subtypes of this slice. Their exact type strings,
// media types, and bounds are part of the wire contract (ADR-0008).
const (
	TypeMarkdown         = "document.markdown.v1"
	MediaTypeMarkdown    = "text/markdown; charset=utf-8"
	TypeUnifiedDiff      = "code.unified-diff.v1"
	MediaTypeUnifiedDiff = "text/x-diff; charset=utf-8"

	// reviewDigestVersion prefixes the canonical review content digest so a
	// future content format can never collide with v1 digests.
	reviewDigestVersion = "workos.review-artifact.v1"
	// outputRequestDigestVersion prefixes the materialization request digest.
	outputRequestDigestVersion = "workos.review-artifact-output.v1"
)

// Review content bounds. They bound memory, rows, and rendering cost; the
// constants must never be relaxed to unbounded values.
const (
	MaxReviewContentBytes  = 512 * 1024
	MaxReviewContentLines  = 20000
	MaxReviewLineBytes     = 16 * 1024
	MaxReviewOutputKeyRuns = 64
)

// ErrOutputConflict marks a materialization whose (task, output key) identity
// was already consumed by a different canonical output. The run fails closed.
var ErrOutputConflict = fmt.Errorf("%w: output key was already used for a different artifact output", ErrIdempotencyConflict)

// ErrCorrupt marks a stored artifact row that fails revalidation on read:
// immutable facts cannot drift, so drift is internal corruption and is
// answered with a sanitized Internal, never InvalidArgument, NotFound, or
// Unavailable.
var ErrCorrupt = errors.New("stored artifact fact failed validation")

// ReviewType resolves a canonical review artifact type string to its stored
// type and server-derived media type. Unknown types are invalid before any
// storage or decoding happens.
func ReviewType(raw string) (artifactType, mediaType string, ok bool) {
	switch raw {
	case TypeMarkdown:
		return TypeMarkdown, MediaTypeMarkdown, true
	case TypeUnifiedDiff:
		return TypeUnifiedDiff, MediaTypeUnifiedDiff, true
	default:
		return "", "", false
	}
}

// IsReviewType reports whether raw names one of the canonical review types.
func IsReviewType(raw string) bool {
	_, _, ok := ReviewType(raw)
	return ok
}

// ValidReviewOutputKey enforces the stable per-task output key grammar:
// lowercase alphanumeric with dot, underscore, and hyphen infixes, starting
// with a letter, at most MaxReviewOutputKeyRuns runes.
func ValidReviewOutputKey(value string) bool {
	if value == "" || len(value) > MaxReviewOutputKeyRuns {
		return false
	}
	for index := 0; index < len(value); index++ {
		c := value[index]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			if index == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// NormalizeReviewTitle trims surrounding Unicode whitespace and returns the
// canonical title, or false when the trimmed result violates the title
// grammar (1..MaxArtifactTitleRunes code points, valid UTF-8, no C0/C1
// controls). Trimming is part of the canonical identity: the normalized
// title is what request digests and rows store.
func NormalizeReviewTitle(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if !ValidArtifactTitle(trimmed) {
		return "", false
	}
	return trimmed, true
}

// NormalizedReviewContent is the canonicalized content fact: exact bytes as
// stored, with the derived counts and digest.
type NormalizedReviewContent struct {
	Content   []byte
	ByteCount int
	LineCount int
	Digest    string
}

// NormalizeReviewContent canonicalizes untrusted provider content bytes:
// valid UTF-8, CRLF normalized to LF, bare CR rejected, NUL and every other
// C0/C1 control rejected except LF and TAB, no trimming and no appended
// trailing newline, at most MaxReviewContentBytes bytes, at most
// MaxReviewContentLines lines, and no line longer than MaxReviewLineBytes
// UTF-8 bytes. The result is byte-identical to what every later read and
// replay verifies against.
func NormalizeReviewContent(artifactType string, raw []byte) (NormalizedReviewContent, error) {
	if _, _, ok := ReviewType(artifactType); !ok {
		return NormalizedReviewContent{}, ErrInvalid
	}
	if len(raw) == 0 || len(raw) > MaxReviewContentBytes {
		return NormalizedReviewContent{}, ErrInvalid
	}
	if !utf8.Valid(raw) {
		return NormalizedReviewContent{}, ErrInvalid
	}
	content := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	for _, b := range content {
		if b == 0 || (b < 0x20 && b != '\n' && b != '\t') || b == 0x7f || (b >= 0x80 && b <= 0x9f) {
			return NormalizedReviewContent{}, ErrInvalid
		}
	}
	lines := countLines(content)
	if lines > MaxReviewContentLines {
		return NormalizedReviewContent{}, ErrInvalid
	}
	for _, line := range bytes.Split(content, []byte("\n")) {
		if len(line) > MaxReviewLineBytes {
			return NormalizedReviewContent{}, ErrInvalid
		}
	}
	return NormalizedReviewContent{
		Content:   content,
		ByteCount: len(content),
		LineCount: lines,
		Digest:    ReviewContentDigest(artifactType, content),
	}, nil
}

// countLines counts LF-separated lines; a trailing LF does not start a new
// empty line. Content is non-empty here, so the count is always >= 1.
func countLines(content []byte) int {
	lines := bytes.Count(content, []byte("\n"))
	if !bytes.HasSuffix(content, []byte("\n")) {
		lines++
	}
	return lines
}

// ReviewContentDigest derives the versioned content digest: a
// length-prefixed encoding of the format version, the canonical artifact
// type, and the normalized content bytes. Length prefixes keep the encoding
// unambiguous; only this function defines the v1 format.
func ReviewContentDigest(artifactType string, content []byte) string {
	digest := sha256.New()
	writeDigestField(digest, reviewDigestVersion)
	writeDigestField(digest, artifactType)
	digest.Write([]byte(fmt.Sprintf("%d:", len(content))))
	digest.Write(content)
	return DigestPrefix + hex.EncodeToString(digest.Sum(nil))
}

// ReviewOutputRequestDigest digests the canonical materialization request
// with an explicit format version. It covers project, task, output key,
// normalized title, and the content digest — every fact that decides replay
// versus conflict for one (task, output key) identity. Provider submission
// order and server-minted identity are deliberately excluded.
func ReviewOutputRequestDigest(projectID, taskID, outputKey, normalizedTitle, contentDigest string) string {
	digest := sha256.New()
	writeDigestField(digest, outputRequestDigestVersion)
	writeDigestField(digest, projectID)
	writeDigestField(digest, taskID)
	writeDigestField(digest, outputKey)
	writeDigestField(digest, normalizedTitle)
	writeDigestField(digest, contentDigest)
	return DigestPrefix + hex.EncodeToString(digest.Sum(nil))
}

func writeDigestField(digest hash.Hash, value string) {
	digest.Write([]byte(fmt.Sprintf("%d:", len(value))))
	digest.Write([]byte(value))
}

// ReviewArtifact is the immutable project review artifact fact. It is
// permanently bound to its owner, project, source task, and provider output
// key; the content bytes travel separately in typed reads only.
type ReviewArtifact struct {
	ID          string
	OwnerUserID string
	ProjectID   string
	SourceTask  string
	OutputKey   string
	Type        string
	Title       string
	MediaType   string
	Digest      string
	ByteCount   int
	LineCount   int
	CreatedAt   time.Time
}

// PublicationRecord is the Core-minted timeline publication reference stored
// beside the adjudication mapping so a replayed materialization returns
// exactly the first published event. The event itself stays owned by the
// Agent module's durable task stream.
type PublicationRecord struct {
	EventID    string
	EventSeq   int64
	OccurredAt time.Time
}

// ValidStoredReviewFact revalidates one stored review artifact row on every
// read and replay: UUID grammar, canonical type/media pairing, title and
// output key grammar, digest shape, and bounded counts. Stored rows are
// immutable, so any drift is internal corruption, never a client error.
func ValidStoredReviewFact(artifact ReviewArtifact) bool {
	if !ValidArtifactUUID(artifact.ID) || artifact.OwnerUserID == "" ||
		!ValidArtifactUUID(artifact.ProjectID) || !ValidArtifactUUID(artifact.SourceTask) ||
		!ValidReviewOutputKey(artifact.OutputKey) {
		return false
	}
	_, expectedMedia, ok := ReviewType(artifact.Type)
	if !ok || expectedMedia != artifact.MediaType {
		return false
	}
	if !ValidArtifactTitle(artifact.Title) || !ValidArtifactDigest(artifact.Digest) {
		return false
	}
	if artifact.ByteCount < 1 || artifact.ByteCount > MaxReviewContentBytes {
		return false
	}
	if artifact.LineCount < 1 || artifact.LineCount > MaxReviewContentLines {
		return false
	}
	return !artifact.CreatedAt.IsZero()
}

// ValidStoredArtifact revalidates one stored metadata row of either subtype
// on every read. Web bundle provenance fields stay empty; review artifacts
// always carry project and source task. Any drift is internal corruption.
func ValidStoredArtifact(artifact Artifact) bool {
	if !ValidArtifactUUID(artifact.ID) || artifact.OwnerUserID == "" ||
		!ValidArtifactTitle(artifact.Title) || !ValidArtifactDigest(artifact.Digest) ||
		artifact.CreatedAt.IsZero() {
		return false
	}
	switch artifact.Type {
	case TypeWebBundle:
		return artifact.MediaType == MediaTypeBundle && artifact.ProjectID == "" &&
			artifact.SourceTaskID == "" && artifact.FileCount >= 1 &&
			artifact.TotalSizeBytes >= 1 && artifact.ContentRef != ""
	case TypeMarkdown, TypeUnifiedDiff:
		_, expectedMedia, ok := ReviewType(artifact.Type)
		return ok && artifact.MediaType == expectedMedia &&
			ValidArtifactUUID(artifact.ProjectID) && ValidArtifactUUID(artifact.SourceTaskID) &&
			artifact.FileCount == 1 && artifact.TotalSizeBytes >= 1
	default:
		return false
	}
}
