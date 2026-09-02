package domain

import (
	"strings"
	"testing"
)

func TestCanonicalQueryAndLexicalUnicode(t *testing.T) {
	t.Parallel()
	got, err := CanonicalQuery("  Résumé\u2003路径  DIFF  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Résumé 路径 DIFF" {
		t.Fatalf("canonical query = %q", got)
	}
	if lexical := LexicalQueryText(got); lexical != "résumé 路径 diff" {
		t.Fatalf("lexical query = %q", lexical)
	}
}

func TestCanonicalQueryRejectsControlsAndBounds(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"", "hello\nworld", "hello\tworld", "hello\x00world", "hello\u0085world", strings.Repeat("x", MaxQueryRunes+1)} {
		if _, err := CanonicalQuery(input); err == nil {
			t.Fatalf("CanonicalQuery(%q) unexpectedly succeeded", input)
		}
	}
	if _, err := CanonicalQuery(strings.Repeat("term ", MaxQueryTerms+1)); err == nil {
		t.Fatal("too many terms unexpectedly succeeded")
	}
	if _, err := CanonicalQuery(strings.Repeat("a-", MaxQueryTerms) + "a"); err == nil {
		t.Fatal("too many punctuation-split lexical terms unexpectedly succeeded")
	}
}

func TestLexicalQueryTextNeutralizesOperators(t *testing.T) {
	t.Parallel()
	if got := LexicalQueryText("alpha:* OR beta'); DROP TABLE docs;--"); got != "alpha or beta drop table docs" {
		t.Fatalf("lexical query = %q", got)
	}
}
