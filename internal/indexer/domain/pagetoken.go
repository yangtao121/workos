// Versioned search page tokens (ADR-0013 §5). A token binds the pagination
// chain to one owner+project, one canonical query digest, one ranking
// version, one projection generation, one snapshot watermark, and the last
// ordered row. Any cross-query/project/snapshot replay or tampering is a
// stable invalid-input verdict — the checksum makes silent mutation
// detectable, and every binding is re-verified against the live request
// before any read.
package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RankingVersion is the fixed lexical ranking algorithm version. Changing
// weights, tie-breaks, or the score formula requires a new version and
// invalidates outstanding tokens by definition.
const RankingVersion = 1

// PageTokenVersion is the wire version of the token envelope.
const PageTokenVersion = 1

var ErrInvalidPageToken = errors.New("search page token is invalid")

// PageToken is the decoded pagination state.
type PageToken struct {
	Version           int       `json:"v"`
	OwnerUserID       string    `json:"owner"`
	ProjectID         string    `json:"project"`
	QueryDigest       string    `json:"q"`
	RankingVersion    int       `json:"rv"`
	GenerationID      string    `json:"gen"`
	SnapshotThrough   time.Time `json:"snap"`
	LastScore         float64   `json:"score"`
	LastSourceCreated time.Time `json:"created"`
	LastSourceID      string    `json:"src"`
	Checksum          string    `json:"sum"`
}

// QueryDigest derives the canonical per-query pagination binding.
func QueryDigest(ownerUserID, projectID, canonicalQuery string) string {
	h := sha256.New()
	h.Write([]byte("workos.index.search-page.v1\n"))
	h.Write([]byte(ownerUserID))
	h.Write([]byte{0})
	h.Write([]byte(projectID))
	h.Write([]byte{0})
	h.Write([]byte(canonicalQuery))
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// EncodePageToken renders the token as opaque URL-safe text.
func EncodePageToken(token PageToken) (string, error) {
	token.Version = PageTokenVersion
	token.Checksum = ""
	payload, err := json.Marshal(token)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	token.Checksum = fmt.Sprintf("%x", sum[:8])
	payload, err = json.Marshal(token)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecodePageToken parses and checksum-verifies one token. Any structural,
// encoding, or checksum failure is the same invalid verdict.
func DecodePageToken(raw string) (PageToken, error) {
	var token PageToken
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return token, ErrInvalidPageToken
	}
	if err := json.Unmarshal(payload, &token); err != nil {
		return token, ErrInvalidPageToken
	}
	stored := token.Checksum
	token.Checksum = ""
	reference, err := json.Marshal(token)
	if err != nil {
		return token, ErrInvalidPageToken
	}
	sum := sha256.Sum256(reference)
	if stored != fmt.Sprintf("%x", sum[:8]) {
		return PageToken{}, ErrInvalidPageToken
	}
	if token.Version != PageTokenVersion || token.RankingVersion != RankingVersion {
		return PageToken{}, ErrInvalidPageToken
	}
	if !ValidUUID(token.OwnerUserID) || !ValidUUID(token.ProjectID) || !ValidUUID(token.GenerationID) || !ValidUUID(token.LastSourceID) {
		return PageToken{}, ErrInvalidPageToken
	}
	if !strings.HasPrefix(token.QueryDigest, "sha256:") {
		return PageToken{}, ErrInvalidPageToken
	}
	if token.LastScore < 0 || IsDisallowedScore(token.LastScore) {
		return PageToken{}, ErrInvalidPageToken
	}
	return token, nil
}

// IsDisallowedScore reports whether a score value may never enter a token or
// a response: NaN and infinities are formatted without a clean parse, so the
// explicit checks live with the formatter.
func IsDisallowedScore(score float64) bool {
	return score != score || score > 1e9
}

// FormatScore renders one score with round-trip-exact precision so a token's
// stored score re-parses bit-identically.
func FormatScore(score float64) string {
	return strconv.FormatFloat(score, 'g', 17, 64)
}

// ScoreBoundUpper is the documented fixed score range [0, ScoreBoundUpper].
// Stored or computed scores outside the range are corruption, not ranking.
const ScoreBoundUpper = 3.0
