package application

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/core/appregistry/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type SourceService struct {
	repository ports.SourceRepository
	ids        ids.Generator
	now        func() time.Time
}

func NewSourceService(repository ports.SourceRepository, generator ids.Generator) (*SourceService, error) {
	if repository == nil || generator == nil {
		return nil, errors.New("app sources require repository and ID generator")
	}
	return &SourceService{repository: repository, ids: generator, now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }}, nil
}
func (s *SourceService) Create(ctx context.Context, owner, key string, files []domain.SourceFile) (domain.SourceBundle, error) {
	if !domain.ValidSourceID(owner) || !domain.ValidIdempotencyKey(key) {
		return domain.SourceBundle{}, domain.ErrInvalid
	}
	normalized, digest, total, err := domain.NormalizeSource(files)
	if err != nil {
		return domain.SourceBundle{}, err
	}
	bundle := domain.SourceBundle{ID: s.ids.New(), OwnerUserID: owner, IdempotencyKey: key, Files: normalized, Digest: digest, TotalSizeBytes: total, CreatedAt: s.now()}
	if err := domain.ValidateSourceBundle(bundle); err != nil {
		return domain.SourceBundle{}, err
	}
	return s.repository.CreateSource(ctx, bundle)
}
func (s *SourceService) Get(ctx context.Context, owner, id string) (domain.SourceBundle, error) {
	if !domain.ValidSourceID(owner) || !domain.ValidSourceID(id) {
		return domain.SourceBundle{}, domain.ErrInvalid
	}
	return s.repository.GetSource(ctx, owner, id)
}
