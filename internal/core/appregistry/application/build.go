package application

import (
	"context"
	"errors"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/core/appregistry/ports"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"github.com/yangtao121/workos/internal/platform/ids"
	"time"
)

var ErrBuildUnavailable = errors.New("app version has no verified build input")

type BuildInput struct {
	Recipe domain.BuildRecipe
	Source domain.SourceBundle
}

type BuildService struct {
	store     ports.BuildStore
	validator ManifestValidator
	ids       ids.Generator
}

func NewBuildService(store ports.BuildStore, validator ManifestValidator, generator ids.Generator) (*BuildService, error) {
	if store == nil || validator == nil || generator == nil {
		return nil, errors.New("app build inputs require store, validator and IDs")
	}
	return &BuildService{store: store, validator: validator, ids: generator}, nil
}

func (s *BuildService) Resolve(ctx context.Context, tx dbtx.Tx, owner, appID, version, digest string) (BuildInput, error) {
	if !domain.ValidSourceID(owner) || !domain.ValidAppID(appID) || !domain.ValidWebBundleArtifactDigest(digest) {
		return BuildInput{}, domain.ErrInvalid
	}
	if _, ok := domain.ParseVersion(version); !ok {
		return BuildInput{}, domain.ErrInvalid
	}
	storedDigest, raw, err := s.store.GetBuildManifest(ctx, tx, owner, appID, version)
	if err != nil {
		return BuildInput{}, err
	}
	manifest, violations := s.validator.Validate(raw)
	if len(raw) == 0 || len(violations) > 0 || manifest.ID != appID || manifest.Version != version || manifest.Digest != storedDigest || manifest.Digest != digest {
		return BuildInput{}, domain.ErrSourceCorrupt
	}
	if manifest.Build == nil {
		return BuildInput{}, ErrBuildUnavailable
	}
	source, err := s.store.GetBuildSource(ctx, tx, owner, manifest.Build.SourceBundleID)
	if err != nil {
		return BuildInput{}, err
	}
	if source.OwnerUserID != owner || source.ID != manifest.Build.SourceBundleID || source.Digest != manifest.Build.SourceDigest {
		return BuildInput{}, domain.ErrSourceCorrupt
	}
	return BuildInput{Recipe: *manifest.Build, Source: source}, nil
}

// Caller holds the task stream lock, so creation and response-loss replay serialize.
func (s *BuildService) SubmitSource(ctx context.Context, tx dbtx.Tx, owner, taskID string, files []domain.SourceFile) (domain.SourceBundle, error) {
	if !domain.ValidSourceID(owner) || !domain.ValidSourceID(taskID) {
		return domain.SourceBundle{}, domain.ErrInvalid
	}
	normalized, digest, size, err := domain.NormalizeSource(files)
	if err != nil {
		return domain.SourceBundle{}, err
	}
	sourceID, err := s.store.FindRepairSource(ctx, tx, owner, taskID)
	if err == nil {
		stored, err := s.store.GetBuildSource(ctx, tx, owner, sourceID)
		if err != nil {
			return domain.SourceBundle{}, err
		}
		if stored.OwnerUserID != owner || stored.ID != sourceID {
			return domain.SourceBundle{}, domain.ErrSourceCorrupt
		}
		if stored.Digest != digest {
			return domain.SourceBundle{}, domain.ErrIdempotencyConflict
		}
		return stored, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.SourceBundle{}, err
	}
	source := domain.SourceBundle{ID: s.ids.New(), OwnerUserID: owner, IdempotencyKey: "repair-source-" + taskID, Files: normalized, Digest: digest, TotalSizeBytes: size, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	source, err = s.store.CreateBuildSource(ctx, tx, source)
	if err != nil {
		return domain.SourceBundle{}, err
	}
	if err := s.store.InsertRepairSource(ctx, tx, owner, taskID, source.ID); err != nil {
		return domain.SourceBundle{}, err
	}
	return source, nil
}
