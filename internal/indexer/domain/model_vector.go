package domain

import "math"

// ModelVector is a derived projection bound to a single immutable model recipe.
type ModelVector struct {
	Values      []float32
	Fingerprint string
}

func (vector ModelVector) Valid() bool {
	if !ValidDigest(vector.Fingerprint) || len(vector.Values) != EmbeddingDimensions {
		return false
	}
	var norm float64
	for _, value := range vector.Values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
		norm += float64(value) * float64(value)
	}
	return math.Abs(norm-1) < 1e-5
}
