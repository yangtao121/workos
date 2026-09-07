// Package embedding provides deterministic test vectors only. Production uses
// the pinned offline model; these fixtures never certify semantic relevance.
package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"github.com/yangtao121/workos/internal/indexer/domain"
	"math"
	"strings"
)

const Fingerprint = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

type Model struct{}

func (Model) Fingerprint() string { return Fingerprint }
func (Model) Query(_ context.Context, text string) ([]float32, error) {
	v := Embed(text)
	return v[:], nil
}
func (m Model) Document(ctx context.Context, text string) ([]float32, error) {
	return m.Query(ctx, text)
}
func Embed(text string) [domain.EmbeddingDimensions]float32 {
	var vec [domain.EmbeddingDimensions]float32
	for _, token := range strings.Fields(strings.ToLower(text)) {
		sum := sha256.Sum256([]byte(token))
		bucket := int(binary.BigEndian.Uint32(sum[0:4]) % domain.EmbeddingDimensions)
		sign := float32(1)
		if sum[4]&1 == 1 {
			sign = -1
		}
		vec[bucket] += sign
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
