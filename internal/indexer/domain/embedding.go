// Package embedding: deterministic local feature-hash embeddings for the
// indexer's semantic slice (ADR-0017). Vectors are 384-dimensional, L2-
// normalized, derived purely from the document text via seeded feature
// hashing — zero external dependencies, fully offline-reproducible. This is
// a bounded lexical-semantics approximation, not a neural model: it captures
// token overlap and rough term frequency, and deliberately claims nothing
// about deep semantic similarity.
package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"strings"
)

// Dimensions is the fixed embedding width.
const Dimensions = 384

// Tokenize splits text into lowercase alphanumeric word tokens.
func Tokenize(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 0x7f && r > ' ')
	})
	return fields
}

// Embed computes the deterministic feature-hash embedding: each token votes
// for one of Dimensions buckets with a +1/-1 sign derived from a second
// hash; the summed vector is L2-normalized. Empty text yields the zero
// vector (which never matches cosine thresholds above 0).
func Embed(text string) [Dimensions]float32 {
	var vec [Dimensions]float32
	for _, token := range Tokenize(text) {
		sum := sha256.Sum256([]byte(token))
		bucket := int(binary.BigEndian.Uint32(sum[0:4]) % Dimensions)
		sign := 1.0
		if sum[4]&1 == 1 {
			sign = -1.0
		}
		vec[bucket] += float32(sign)
	}
	norm := 0.0
	for i := range vec {
		norm += float64(vec[i]) * float64(vec[i])
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return vec
	}
	for i := range vec {
		vec[i] /= float32(norm)
	}
	return vec
}

// Cosine returns the cosine similarity of two vectors.
func Cosine(a, b *[Dimensions]float32) float32 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}
