package ports

import (
	"context"
	"errors"
)

var ErrEmbeddingUnavailable = errors.New("local embedding is unavailable")

// EmbeddingModel keeps inference and model identity outside the domain and store.
// Results have exactly 384 finite, normalized values; input is bounded UTF-8.
type EmbeddingModel interface {
	Query(context.Context, string) ([]float32, error)
	Document(context.Context, string) ([]float32, error)
	Fingerprint() string
}
