package ports

import (
	"context"
	"errors"

	"github.com/yangtao121/workos/internal/indexer/domain"
)

var ErrEmbeddingUnavailable = errors.New("local embedding is unavailable")

// EmbeddingModel keeps inference and model identity outside the domain and store.
// Results have exactly 384 finite, normalized values; input is bounded UTF-8.
type EmbeddingModel interface {
	Query(context.Context, string) ([]float32, error)
	Document(context.Context, string) ([]float32, error)
	Fingerprint() string
}

// EmbeddingSnapshot is a digest/publication-pinned input for cache backfill.
type EmbeddingSnapshot struct {
	GenerationID string
	Document     domain.Document
}

type EmbeddingStore interface {
	MissingEmbeddings(context.Context, string, int) ([]EmbeddingSnapshot, error)
	StoreEmbedding(context.Context, EmbeddingSnapshot, domain.ModelVector) (bool, error)
}
