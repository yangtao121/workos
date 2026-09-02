// Excerpt extraction (ADR-0013 §5): a pure, deterministic window over the
// canonical document content. The excerpt is bounded plain text — never
// HTML — with newline/control normalization and code-point-safe truncation.
// The same (content, match) always yields the same excerpt.
package domain

import (
	"strings"
	"unicode/utf8"
)

// Excerpt bounds. The window is small enough that a full result page of
// excerpts is bounded regardless of the underlying document size.
const (
	MaxExcerptRunes     = 200
	ExcerptContextRunes = 60 // code points of context kept on each side of a match
)

// ExcerptTerms are the lowercase lexical terms to locate in the content.
// Search callers pass the canonical query's whitespace-split lowercase
// terms; empty terms are ignored.
type ExcerptRequest struct {
	Content string
	Terms   []string
}

// BuildExcerpt returns the bounded excerpt for one document. Behaviour:
//   - find the first content occurrence of any term (case-insensitive);
//   - keep up to ExcerptContextRunes of context on each side;
//   - expand both edges to whitespace boundaries when possible;
//   - normalize CR/LF/TAB runs to single spaces and strip other C0/C1;
//   - never split a code point, never exceed MaxExcerptRunes;
//   - with no content match (title-only hit), excerpt the head of the
//     content with the same normalization.
func BuildExcerpt(request ExcerptRequest) string {
	content := request.Content
	if content == "" {
		return ""
	}
	start, end := -1, -1
	lower := strings.ToLower(content)
	for _, term := range request.Terms {
		if term == "" {
			continue
		}
		if idx := strings.Index(lower, term); idx >= 0 {
			if start == -1 || idx < start {
				start = idx
			}
			if stop := idx + len(term); stop > end {
				end = stop
			}
		}
	}
	var window string
	if start == -1 {
		window = truncateRunes(content, MaxExcerptRunes)
	} else {
		runes := []rune(content)
		// Convert byte offsets to rune offsets for safe windowing.
		startRune := utf8.RuneCountInString(content[:start])
		endRune := startRune + utf8.RuneCountInString(content[start:end])
		lo := startRune - ExcerptContextRunes
		if lo < 0 {
			lo = 0
		}
		hi := endRune + ExcerptContextRunes
		if hi > len(runes) {
			hi = len(runes)
		}
		if hi-lo > MaxExcerptRunes {
			// Center the match inside the maximum window.
			lo = startRune - (MaxExcerptRunes-1)/2
			if lo < 0 {
				lo = 0
			}
			hi = lo + MaxExcerptRunes
			if hi > len(runes) {
				hi = len(runes)
				lo = hi - MaxExcerptRunes
				if lo < 0 {
					lo = 0
				}
			}
		}
		// Snap to whitespace boundaries where the room exists.
		for lo > 0 && !isSpaceRune(runes[lo]) {
			lo--
		}
		for lo > 0 && isSpaceRune(runes[lo]) {
			lo++
		}
		for hi < len(runes) && !isSpaceRune(runes[hi]) {
			hi++
		}
		window = string(runes[lo:hi])
	}
	return normalizeExcerptText(window)
}

// normalizeExcerptText collapses CR/LF/TAB to single spaces and removes all
// other C0/C1 control characters so an excerpt can never carry control bytes
// into the DOM.
func normalizeExcerptText(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	lastSpace := false
	for _, r := range value {
		switch {
		case r == ' ':
			b.WriteRune(' ')
			lastSpace = true
			continue
		case r == '\t' || r == '\n' || r == '\r':
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
			continue
		case (r >= 0x00 && r <= 0x1F) || (r >= 0x7F && r <= 0x9F):
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}
	return strings.TrimSpace(b.String())
}

func isSpaceRune(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
