// Search read-model facts (ADR-0013 §5): one bounded, deterministic lexical
// page over the active projection generation. Scores are finite with a fixed
// range and deterministic tie-break; hits carry only safe projections.
package domain

import "time"

// SearchQuery is one fully validated search command.
type SearchQuery struct {
	OwnerUserID    string
	ProjectID      string
	CanonicalQuery string
	QueryDigest    string
	PageSize       int
	// Decoded continuation state; empty TokenRaw means first page.
	TokenRaw string
	Decoded  *PageToken
}

// SearchHit is one projected hit: safe fields only. There is no full text,
// no internal row id, no owner id, no lease/publication token.
type SearchHit struct {
	ContextRef   string // canonical "artifact.review.v1:<id>:<digest>" projection
	Excerpt      string
	Score        float64
	ArtifactID   string
	ArtifactType string
	Digest       string
	Title        string
	CreatedAt    time.Time
}

// SearchPage is one explicit page plus the continuation decided by the
// limit+1 probe. A full final page produces no phantom token.
type SearchPage struct {
	Hits            []SearchHit
	NextPageToken   string
	Continuation    *PageToken
	GenerationID    string
	SnapshotThrough time.Time
}

// Freshness is the bounded freshness projection served with search results.
// It is derived from durable consumer facts — never a fixed READY.
type Freshness struct {
	CaughtUp            bool
	IndexedThrough      time.Time
	LastIndexedAt       time.Time
	PendingPublications int64
}

// ContextRefString renders the canonical string projection of a typed
// artifact.review.v1 ref (the legacy SearchHit.context_ref grammar).
func ContextRefString(artifactID, digest string) string {
	return "artifact.review.v1:" + artifactID + ":" + digest
}
