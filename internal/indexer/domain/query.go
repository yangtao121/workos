// Lexical query grammar for the project knowledge search (ADR-0013 §5).
// Validation happens before any business read: malformed, oversized, or
// control-bearing queries are rejected as invalid input and never reach the
// store. The canonical form is deterministic so the page-token query digest
// is stable across replays.
package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Query bounds. They bound decode cost, tsquery construction, and wire
// bodies; the constants must never be relaxed to unbounded values.
const (
	MinQueryRunes = 1
	MaxQueryRunes = 256
	MaxQueryTerms = 32
)

var ErrInvalidQuery = errors.New("search query is invalid")

// CanonicalQuery validates and canonicalizes one lexical query: valid UTF-8,
// no C0/C1 control characters (whitespace is normalized, not rejected), trim
// + whitespace collapse, 1..256 code points, at most 32 whitespace-separated
// terms. The canonical form is what gets searched and digested.
func CanonicalQuery(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrInvalidQuery
	}
	for _, r := range raw {
		if (r >= 0x00 && r <= 0x1F) || (r >= 0x7F && r <= 0x9F) {
			if r == '\t' || r == '\n' || r == '\r' || r == ' ' {
				continue
			}
			return "", ErrInvalidQuery
		}
	}
	collapsed := strings.Join(strings.Fields(raw), " ")
	if utf8.RuneCountInString(collapsed) < MinQueryRunes || utf8.RuneCountInString(collapsed) > MaxQueryRunes {
		return "", ErrInvalidQuery
	}
	if terms := strings.Fields(collapsed); len(terms) > MaxQueryTerms {
		return "", ErrInvalidQuery
	}
	return collapsed, nil
}

// Search bounds: default and maximum page size (ADR-0013 §5).
const (
	DefaultSearchPageSize = 20
	MaxSearchPageSize     = 50
	MinSearchPageSize     = 1
)

// ClampSearchPageSize normalizes one requested page size exactly once.
func ClampSearchPageSize(requested int32) int {
	if requested <= 0 {
		return DefaultSearchPageSize
	}
	if int(requested) > MaxSearchPageSize {
		return MaxSearchPageSize
	}
	return int(requested)
}
