package domain

import (
	"strings"
)

// Canonical agent context reference facts (ADR-0010). The first version
// supports exactly one ref type: an immutable project review artifact
// pinned by its exact content digest.
const (
	ContextRefTypeArtifactReviewV1 = "artifact.review.v1"
	ContextDigestPrefix            = "sha256:"
	// MaxContextRefs bounds one task's context set; request order is the
	// durable order.
	MaxContextRefs = 4
)

// ContextRef mirrors the canonical wire triple the task input persists. The
// revision field carries the exact immutable artifact digest
// ("sha256:" + 64 lowercase hex) — never a mutable revision counter.
type ContextRef struct {
	Type     string
	ID       string
	Revision string
}

// ValidContextRefType reports whether the ref type is a canonical type this
// version defines.
func ValidContextRefType(value string) bool {
	return value == ContextRefTypeArtifactReviewV1
}

// ValidContextRefDigest enforces the exact pinned-digest grammar.
func ValidContextRefDigest(value string) bool {
	if !strings.HasPrefix(value, ContextDigestPrefix) {
		return false
	}
	hex := value[len(ContextDigestPrefix):]
	if len(hex) != 64 {
		return false
	}
	for _, c := range []byte(hex) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidateContextRefs enforces the full context-set grammar: canonical
// types, canonical UUIDv7 IDs, exact digests, at most MaxContextRefs, no
// duplicate (type, id, revision) triples, and no same artifact pinned at two
// different digests (an ambiguous contradiction, not a set).
func ValidateContextRefs(refs []ContextRef) error {
	if len(refs) > MaxContextRefs {
		return ErrInvalid
	}
	seen := make(map[string]struct{}, len(refs))
	ids := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if !ValidContextRefType(ref.Type) || !ValidAppTaskUUID(ref.ID) || ref.ID != strings.ToLower(ref.ID) {
			return ErrInvalid
		}
		if !ValidContextRefDigest(ref.Revision) {
			return ErrInvalid
		}
		key := ref.Type + "\x1f" + ref.ID + "\x1f" + ref.Revision
		if _, dup := seen[key]; dup {
			return ErrInvalid
		}
		seen[key] = struct{}{}
		if _, sameID := ids[ref.ID]; sameID {
			// Same artifact at two different digests is a contradiction.
			return ErrInvalid
		}
		ids[ref.ID] = struct{}{}
	}
	return nil
}
