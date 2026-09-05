// Generic archive application service (ADR-0017 §5): bounded,
// content-addressed object facts for the local operator. Objects are
// opaque bytes — never indexed into search, never a knowledge source —
// and every bound violation fails closed.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
)

// ErrArchiveFull rejects a put when the owner already holds the bounded
// maximum number of objects.
var ErrArchiveFull = errors.New("archive object bound reached for this owner")

// ArchiveService is the minimal put/get/list surface over the store.
type ArchiveService struct {
	store ports.ArchiveStore
}

// NewArchiveService wires the archive service.
func NewArchiveService(store ports.ArchiveStore) (*ArchiveService, error) {
	if store == nil {
		return nil, errors.New("archive service requires the store")
	}
	return &ArchiveService{store: store}, nil
}

// digestBytes mirrors the canonical sha256 grammar used across the indexer.
func digestBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Put validates the bounds, dedupes by owner+digest, and enforces the
// per-owner object count before storing.
func (s *ArchiveService) Put(ctx context.Context, ownerUserID, mediaType string, content []byte) (ports.ArchiveObject, bool, error) {
	if !domain.ValidUUID(ownerUserID) || len(content) == 0 || len(content) > domain.ArchiveMaxObjectBytes {
		return ports.ArchiveObject{}, false, domain.ErrInvalid
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	if !domain.ValidArchiveMediaType(mediaType) {
		return ports.ArchiveObject{}, false, domain.ErrInvalid
	}
	count, err := s.store.CountArchiveObjects(ctx, ownerUserID)
	if err != nil {
		return ports.ArchiveObject{}, false, err
	}
	if count >= domain.ArchiveMaxObjects {
		return ports.ArchiveObject{}, false, fmt.Errorf("%w: %d objects", ErrArchiveFull, domain.ArchiveMaxObjects)
	}
	digest := digestBytes(content)
	return s.store.PutArchiveObject(ctx, ownerUserID, digest, mediaType, content, time.Now().UTC())
}

// Get reads one object's metadata and bytes.
func (s *ArchiveService) Get(ctx context.Context, ownerUserID, objectID string) (ports.ArchiveObject, []byte, error) {
	return s.store.GetArchiveObject(ctx, ownerUserID, objectID)
}

// List reads one bounded metadata page.
func (s *ArchiveService) List(ctx context.Context, ownerUserID string, limit int) ([]ports.ArchiveObject, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.store.ListArchiveObjects(ctx, ownerUserID, limit)
}

// ArchivePutResult is the sanitized put outcome.
type ArchivePutResult struct {
	Object   ports.ArchiveObject
	Inserted bool
}

// PutResult wraps the store put for the admin surface.
func (s *ArchiveService) PutResult(ctx context.Context, ownerUserID, mediaType string, content []byte) (ArchivePutResult, error) {
	object, inserted, err := s.Put(ctx, ownerUserID, mediaType, content)
	if err != nil {
		return ArchivePutResult{}, err
	}
	return ArchivePutResult{Object: object, Inserted: inserted}, nil
}
