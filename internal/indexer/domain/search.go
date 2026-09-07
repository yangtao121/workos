// Search read-model facts (ADR-0013 §5): one bounded, deterministic lexical
// page over the active projection generation. Scores are finite with a fixed
// range and deterministic tie-break; hits carry only safe projections.
package domain

import "time"

// SearchQuery is one fully validated search command.
type SearchQuery struct {
	Embedding      ModelVector
	SourceType     string
	OwnerUserID    string
	ProjectID      string
	CanonicalQuery string
	QueryDigest    string
	PageSize       int
	// Ranking selects the deterministic ordering: RankingLexical (ADR-0013)
	// or RankingHybrid (ADR-0017). A page token from one ranking never
	// paginates another.
	Ranking int
	// Decoded continuation state; empty TokenRaw means first page.
	TokenRaw string
	Decoded  *PageToken
}

// ValidRanking reports whether r names a defined ranking algorithm.
func ValidRanking(r int) bool { return r == RankingLexical || r == RankingHybrid }

// SearchHit is one projected hit: safe fields only. There is no full text,
// no internal row id, no owner id, no lease/publication token.
type SearchHit struct {
	ContextRef   string // canonical "<source_type>:<id>:<digest>" projection
	Excerpt      string
	Score        float64
	ArtifactID   string
	SourceType   string // "artifact.review.v1" or "workspace.file.v1"
	ArtifactType string
	Digest       string
	Title        string
	CreatedAt    time.Time
}

// Source types are the documented provenance vocabulary of indexed
// documents (ADR-0013 §4, ADR-0017 §4).
const (
	SourceReviewArtifact = "artifact.review.v1"
	SourceWorkspaceFile  = "workspace.file.v1"
)

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
	return ContextRef(SourceReviewArtifact, artifactID, digest)
}

// ContextRef renders the canonical "<source_type>:<id>:<digest>" projection
// for any documented source type.
func ContextRef(sourceType, artifactID, digest string) string {
	return sourceType + ":" + artifactID + ":" + digest
}

// DocumentRead identifies one immutable snapshot in the current projection.
type DocumentRead struct {
	OwnerUserID string
	ProjectID   string
	SourceType  string
	SourceID    string
	Digest      string
}
