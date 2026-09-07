package application

import (
	"context"
	"slices"
	"time"

	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
)

// ModelProjectionStore is the Indexer's local projection boundary. Inference
// happens before its transactional methods, never while holding database locks.
type ModelProjectionStore interface {
	ports.ProjectionRepository
	ports.WorkspaceStore
	ports.ArchiveStore
	ports.EmbeddingStore
	ActiveGenerationStatus(context.Context) (domain.GenerationStatus, error)
}

type ModelProjection struct {
	ModelProjectionStore
	model ports.EmbeddingModel
}

func NewModelProjection(store ModelProjectionStore, model ports.EmbeddingModel) (*ModelProjection, error) {
	if store == nil || model == nil || !domain.ValidDigest(model.Fingerprint()) || store.ModelFingerprint() != model.Fingerprint() {
		return nil, errServiceWiring("model projection requires storage and a pinned model")
	}
	return &ModelProjection{ModelProjectionStore: store, model: model}, nil
}

func documentVector(ctx context.Context, model ports.EmbeddingModel, title, content string) (domain.ModelVector, error) {
	values, err := model.Document(ctx, title+"\n"+content)
	if err != nil {
		return domain.ModelVector{}, err
	}
	vector := domain.ModelVector{Values: values, Fingerprint: model.Fingerprint()}
	if !vector.Valid() {
		return domain.ModelVector{}, ports.ErrEmbeddingUnavailable
	}
	return vector, nil
}

func (p *ModelProjection) ApplyResolvedSource(ctx context.Context, source ports.ResolvedSource, outcome, digest string, now time.Time) error {
	if outcome == domain.OutcomeApplied {
		vector, err := documentVector(ctx, p.model, source.Title, string(source.Content))
		if err != nil {
			return err
		}
		source.Embedding = vector
	}
	return p.ModelProjectionStore.ApplyResolvedSource(ctx, source, outcome, digest, now)
}

func (p *ModelProjection) SearchHybrid(ctx context.Context, query domain.SearchQuery) (domain.SearchPage, error) {
	if domain.LexicalQueryText(query.CanonicalQuery) == "" {
		return p.ModelProjectionStore.SearchHybrid(ctx, query)
	}
	values, err := p.model.Query(ctx, query.CanonicalQuery)
	if err != nil {
		return domain.SearchPage{}, err
	}
	query.Embedding = domain.ModelVector{Values: values, Fingerprint: p.model.Fingerprint()}
	if !query.Embedding.Valid() {
		return domain.SearchPage{}, ports.ErrEmbeddingUnavailable
	}
	return p.ModelProjectionStore.SearchHybrid(ctx, query)
}

func (p *ModelProjection) ConvergeWorkspacePass(ctx context.Context, source ports.WorkspaceSource, files []ports.MountFile, skipped int64, publication func() string, now time.Time) (ports.WorkspaceSource, int64, int64, error) {
	if len(files) > domain.WorkspaceMaxFiles {
		return ports.WorkspaceSource{}, 0, 0, domain.ErrInvalid
	}
	files = slices.Clone(files)
	for i := range files {
		vector, err := documentVector(ctx, p.model, files[i].Title, string(files[i].Content))
		if err != nil {
			return ports.WorkspaceSource{}, 0, 0, err
		}
		files[i].Embedding = vector
	}
	return p.ModelProjectionStore.ConvergeWorkspacePass(ctx, source, files, skipped, publication, now)
}

// Backfill advances durable progress through each row's model fingerprint. A
// concurrent archive, sync, or generation cleanup makes the conditional write
// a no-op. No source filesystem or Core mutation is needed to rebuild vectors.
func (p *ModelProjection) Backfill(ctx context.Context) (int, error) {
	snapshots, err := p.MissingEmbeddings(ctx, p.model.Fingerprint(), 8)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, snapshot := range snapshots {
		if domain.ValidStoredDocument(snapshot.Document) != nil {
			return count, domain.ErrCorrupt
		}
		vector, err := documentVector(ctx, p.model, snapshot.Document.Title, snapshot.Document.Content)
		if err != nil {
			return count, err
		}
		applied, err := p.StoreEmbedding(ctx, snapshot, vector)
		if err != nil {
			return count, err
		}
		if applied {
			count++
		}
	}
	return count, nil
}

type ModelRebuildStore struct {
	RebuildStore
	model ports.EmbeddingModel
}

func NewModelRebuildStore(store RebuildStore, model ports.EmbeddingModel) (*ModelRebuildStore, error) {
	if store == nil || model == nil || !domain.ValidDigest(model.Fingerprint()) {
		return nil, errServiceWiring("model rebuild requires storage and a pinned model")
	}
	return &ModelRebuildStore{RebuildStore: store, model: model}, nil
}

func (s *ModelRebuildStore) ApplySnapshotSource(ctx context.Context, effect SnapshotEffect, generation, digest string, now time.Time) error {
	if !effect.Tombstone {
		vector, err := documentVector(ctx, s.model, effect.Title, string(effect.Content))
		if err != nil {
			return err
		}
		effect.Embedding = vector
	}
	return s.RebuildStore.ApplySnapshotSource(ctx, effect, generation, digest, now)
}
