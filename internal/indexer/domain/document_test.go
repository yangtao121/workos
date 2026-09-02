package domain

import (
	"math"
	"strings"
	"testing"
	"time"
)

func validDocumentFixture() Document {
	return Document{
		OwnerUserID:  "01999999-9999-7999-8999-000000000011",
		ProjectID:    "01999999-9999-7999-8999-000000000012",
		SourceID:     "01999999-9999-7999-8999-000000000013",
		SourceDigest: "sha256:" + strings.Repeat("a", 64),
		ArtifactType: "document.markdown.v1", Title: "Résumé 路径",
		Content: "# Résumé\n@@ -1 +1 @@\n", SourceCreatedAt: time.Unix(1, 0).UTC(),
		LastPublication: "01999999-9999-7999-8999-000000000014", IndexedAt: time.Unix(2, 0).UTC(),
	}
}

func TestValidStoredDocumentRejectsMalformedProjection(t *testing.T) {
	t.Parallel()
	if err := ValidStoredDocument(validDocumentFixture()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Document){
		"title control": func(d *Document) { d.Title = "bad\nname" },
		"content nul":   func(d *Document) { d.Content = "bad\x00body" },
		"content bytes": func(d *Document) { d.Content = strings.Repeat("界", 200000) },
		"non-v7":        func(d *Document) { d.SourceID = "550e8400-e29b-41d4-a716-446655440000" },
	} {
		document := validDocumentFixture()
		mutate(&document)
		if err := ValidStoredDocument(document); err != ErrCorrupt {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

func TestValidStoredScoreRejectsNonFiniteAndOutOfRange(t *testing.T) {
	t.Parallel()
	for _, score := range []float64{-1, ScoreBoundUpper + 0.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := ValidStoredScore(score); err != ErrCorrupt {
			t.Fatalf("score %v: error = %v", score, err)
		}
	}
}
