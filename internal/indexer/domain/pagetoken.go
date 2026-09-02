// Versioned search page tokens (ADR-0013 §5). A token binds the pagination
// chain to one owner+project, one canonical query digest, one ranking
// version, one projection generation, one snapshot watermark, and the last
// ordered row. Any cross-query/project/snapshot replay or tampering is a
// stable invalid-input verdict — the HMAC signature makes mutation
// detectable without exposing a signing oracle, and every binding is
// re-verified against the live request before any read.
package domain

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"
)

// RankingVersion is the fixed lexical ranking algorithm version. Changing
// weights, tie-breaks, or the score formula requires a new version and
// invalidates outstanding tokens by definition.
const RankingVersion = 1

// PageTokenVersion is the wire version of the token envelope.
const PageTokenVersion = 1

var ErrInvalidPageToken = errors.New("search page token is invalid")

// PageTokenCodec signs opaque pagination state with an indexer-only key.
// The key must be stable across restarts and never leaves the indexer.
type PageTokenCodec struct {
	key []byte
}

func NewPageTokenCodec(key []byte) (PageTokenCodec, error) {
	if len(key) < 32 || len(key) > 1024 {
		return PageTokenCodec{}, errors.New("search page token key must be between 32 and 1024 bytes")
	}
	return PageTokenCodec{key: append([]byte(nil), key...)}, nil
}

func (c PageTokenCodec) Valid() bool { return len(c.key) >= 32 && len(c.key) <= 1024 }

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
	Signature         string    `json:"sig"`
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

// Encode renders the token as signed opaque URL-safe text.
func (c PageTokenCodec) Encode(token PageToken) (string, error) {
	if len(c.key) == 0 {
		return "", ErrInvalidPageToken
	}
	token.Version = PageTokenVersion
	token.Signature = ""
	payload, err := json.Marshal(token)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(payload)
	token.Signature = fmt.Sprintf("%x", mac.Sum(nil))
	payload, err = json.Marshal(token)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// Decode parses and authenticates one token. Any structural, encoding, or
// signature failure is the same invalid verdict.
func (c PageTokenCodec) Decode(raw string) (PageToken, error) {
	var token PageToken
	if len(c.key) == 0 || len(raw) == 0 || len(raw) > 4096 {
		return token, ErrInvalidPageToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return token, ErrInvalidPageToken
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&token); err != nil {
		return token, ErrInvalidPageToken
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return token, ErrInvalidPageToken
	}
	stored := token.Signature
	token.Signature = ""
	reference, err := json.Marshal(token)
	if err != nil {
		return token, ErrInvalidPageToken
	}
	provided, err := decodeHexSHA256(stored)
	if err != nil {
		return PageToken{}, ErrInvalidPageToken
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(reference)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return PageToken{}, ErrInvalidPageToken
	}
	if token.Version != PageTokenVersion || token.RankingVersion != RankingVersion {
		return PageToken{}, ErrInvalidPageToken
	}
	if !ValidUUID(token.OwnerUserID) || !ValidUUID(token.ProjectID) || !ValidUUID(token.GenerationID) || !ValidUUID(token.LastSourceID) {
		return PageToken{}, ErrInvalidPageToken
	}
	if !ValidDigest(token.QueryDigest) {
		return PageToken{}, ErrInvalidPageToken
	}
	if token.LastScore < 0 || token.LastScore > ScoreBoundUpper || IsDisallowedScore(token.LastScore) {
		return PageToken{}, ErrInvalidPageToken
	}
	_, snapshotOffset := token.SnapshotThrough.Zone()
	_, createdOffset := token.LastSourceCreated.Zone()
	if token.SnapshotThrough.IsZero() || token.LastSourceCreated.IsZero() ||
		snapshotOffset != 0 || createdOffset != 0 {
		return PageToken{}, ErrInvalidPageToken
	}
	return token, nil
}

func decodeHexSHA256(value string) ([]byte, error) {
	if len(value) != sha256.Size*2 {
		return nil, ErrInvalidPageToken
	}
	decoded := make([]byte, sha256.Size)
	for index := range decoded {
		high, ok := hexNibble(value[index*2])
		if !ok {
			return nil, ErrInvalidPageToken
		}
		low, ok := hexNibble(value[index*2+1])
		if !ok {
			return nil, ErrInvalidPageToken
		}
		decoded[index] = high<<4 | low
	}
	return decoded, nil
}

func hexNibble(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	default:
		return 0, false
	}
}

// IsDisallowedScore reports whether a score value may never enter a token or
// a response: NaN and infinities are formatted without a clean parse, so the
// explicit checks live with the formatter.
func IsDisallowedScore(score float64) bool {
	return math.IsNaN(score) || math.IsInf(score, 0)
}

// FormatScore renders one score with round-trip-exact precision so a token's
// stored score re-parses bit-identically.
func FormatScore(score float64) string {
	return strconv.FormatFloat(score, 'g', 17, 64)
}

// ScoreBoundUpper is the documented fixed score range [0, ScoreBoundUpper].
// Stored or computed scores outside the range are corruption, not ranking.
const ScoreBoundUpper = 3.0
