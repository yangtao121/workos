package domain

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestEmbedIsDeterministicL2NormalizedAndDiscriminating(t *testing.T) {
	t.Parallel()
	text := "Deploy the notification relay before the indexer restart"
	first := Embed(text)
	second := Embed(text)
	if first != second {
		t.Fatal("embedding is not deterministic for identical text")
	}
	var norm float64
	for i := range first {
		norm += float64(first[i]) * float64(first[i])
	}
	if math.Abs(norm-1) > 1e-5 {
		t.Fatalf("embedding is not L2-normalized: norm=%v", norm)
	}
	same := Embed("deploy the NOTIFICATION relay before the indexer restart")
	if CosineSimilarity(first, same) < 0.99 {
		t.Fatalf("case-insensitive same-text cosine = %v", CosineSimilarity(first, same))
	}
	unrelated := Embed("quantum pottery glaze recipes for desert climates")
	if CosineSimilarity(first, unrelated) > 0.2 {
		t.Fatalf("unrelated text cosine too high: %v", CosineSimilarity(first, unrelated))
	}
	if got := Embed(""); got != ([EmbeddingDimensions]float32{}) {
		t.Fatal("empty text must yield the zero vector")
	}
	if CosineSimilarity(first, [EmbeddingDimensions]float32{}) != 0 {
		t.Fatal("zero-vector cosine must be 0")
	}
}

func TestHybridRankingVersionRoundTripsAndLexicalStillValid(t *testing.T) {
	t.Parallel()
	codec, err := NewPageTokenCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	base := PageToken{
		OwnerUserID: "01999999-9999-7999-8999-000000000001",
		ProjectID:   "01999999-9999-7999-8999-000000000002",
		QueryDigest: "sha256:" + strings.Repeat("a", 64),
	}
	for _, ranking := range []int{RankingLexical, RankingHybrid} {
		token := base
		token.RankingVersion = ranking
		token.GenerationID = "01999999-9999-7999-8999-000000000003"
		token.SnapshotThrough = time.Unix(10, 0).UTC()
		token.LastScore = 0.75
		token.LastSourceCreated = time.Unix(9, 0).UTC()
		token.LastSourceID = "01999999-9999-7999-8999-000000000004"
		raw, err := codec.Encode(token)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := codec.Decode(raw)
		if err != nil {
			t.Fatalf("ranking %d token rejected: %v", ranking, err)
		}
		if decoded.RankingVersion != ranking {
			t.Fatalf("ranking version drifted: %d", decoded.RankingVersion)
		}
	}
	unknown := base
	unknown.RankingVersion = 99
	unknown.GenerationID = "01999999-9999-7999-8999-000000000003"
	unknown.SnapshotThrough = time.Unix(10, 0).UTC()
	unknown.LastScore = 0.75
	unknown.LastSourceCreated = time.Unix(9, 0).UTC()
	unknown.LastSourceID = "01999999-9999-7999-8999-000000000004"
	raw, err := codec.Encode(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Decode(raw); err != ErrInvalidPageToken {
		t.Fatalf("unknown ranking version accepted: %v", err)
	}
	if !ValidRanking(RankingLexical) || !ValidRanking(RankingHybrid) || ValidRanking(99) {
		t.Fatal("ValidRanking verdict wrong")
	}
}
