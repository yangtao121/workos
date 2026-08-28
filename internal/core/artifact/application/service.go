// Package application holds the Artifact module's use cases. Cross-module
// consumers define neutral ports and reach this service only through the
// orchestration layer.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/core/artifact/domain"
	"github.com/yangtao121/workos/internal/core/artifact/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

const (
	defaultPageSize = 50
	maxPageSize     = 100
)

// Service executes the web bundle artifact use cases: bounded create with
// durable idempotency, owner-scoped metadata reads, and single-file reads
// for the installed-instance resolver.
type Service struct {
	repository ports.Repository
	ids        ids.Generator
	now        func() time.Time
}

func New(repository ports.Repository, generator ids.Generator) (*Service, error) {
	if repository == nil || generator == nil {
		return nil, errors.New("artifact service requires repository and id generator")
	}
	return &Service{repository: repository, ids: generator, now: func() time.Time { return time.Now().UTC() }}, nil
}

// CreateWebBundle validates, normalizes, and persists one immutable owner-
// scoped web bundle artifact. The canonical request digest is order-
// independent; validation failures never consume the key.
func (s *Service) CreateWebBundle(ctx context.Context, ownerUserID, idempotencyKey, title string, entrypoint string, files []domain.BundleFileInput) (domain.Artifact, error) {
	if ownerUserID == "" || !domain.ValidArtifactIdempotencyKey(idempotencyKey) || !domain.ValidArtifactTitle(title) {
		return domain.Artifact{}, domain.ErrInvalid
	}
	bundle, err := domain.NormalizeWebBundle(entrypoint, files)
	if err != nil {
		return domain.Artifact{}, err
	}
	total := 0
	for _, file := range bundle.Files {
		total += file.SizeBytes
	}
	record := domain.Artifact{
		ID: s.ids.New(), OwnerUserID: ownerUserID,
		Type: domain.TypeWebBundle, Title: title, MediaType: domain.MediaTypeBundle,
		ContentRef: "wbbnd:" + s.ids.New(), Digest: bundle.CanonicalDigest(),
		Entrypoint: bundle.Entrypoint, FileCount: len(bundle.Files),
		TotalSizeBytes: int64(total), CreatedAt: s.now(),
	}
	return s.repository.Create(ctx, ports.CreateCommand{
		Artifact: record, Bundle: bundle, IdempotencyKey: idempotencyKey,
		RequestDigest: domain.CreateRequestDigest(title, record.Digest),
	})
}

// Get returns one owner-scoped artifact's metadata. Public reads never
// include file bytes.
func (s *Service) Get(ctx context.Context, ownerUserID, artifactID string) (domain.Artifact, error) {
	if ownerUserID == "" || !domain.ValidArtifactUUID(artifactID) {
		return domain.Artifact{}, domain.ErrInvalid
	}
	return s.repository.Get(ctx, ownerUserID, artifactID)
}

// List returns the owner's artifacts ordered by ID, one page at a time. The
// page size is normalized exactly once here; only owner-scoped listing is
// implemented, so a project filter is rejected.
func (s *Service) List(ctx context.Context, ownerUserID, projectID, cursor string, pageSize int) (ports.PageResult, error) {
	if ownerUserID == "" {
		return ports.PageResult{}, domain.ErrInvalid
	}
	if projectID != "" {
		return ports.PageResult{}, domain.ErrUnsupported
	}
	switch {
	case pageSize < 0:
		return ports.PageResult{}, domain.ErrInvalid
	case pageSize == 0:
		pageSize = defaultPageSize
	case pageSize > maxPageSize:
		pageSize = maxPageSize
	}
	if cursor != "" && !domain.ValidArtifactUUID(cursor) {
		return ports.PageResult{}, domain.ErrInvalid
	}
	idsPage, nextCursor, err := s.repository.ListIDsPage(ctx, ownerUserID, cursor, pageSize)
	if err != nil {
		return ports.PageResult{}, err
	}
	items := make([]domain.Artifact, 0, len(idsPage))
	if err := s.repository.VisitSummaries(ctx, ownerUserID, idsPage, func(artifact domain.Artifact) error {
		items = append(items, artifact)
		return nil
	}); err != nil {
		return ports.PageResult{}, err
	}
	return ports.PageResult{Items: items, NextToken: nextCursor}, nil
}

// BundleSummary is the neutral verification result for cross-module callers:
// the entrypoint of a verified web bundle artifact.
type BundleSummary struct {
	Entrypoint string
}

// VerifyWebBundle proves the artifact is the owner's exact web bundle with
// the expected canonical digest and returns its entrypoint. Foreign or
// unknown artifacts are ErrNotFound; a digest drift is ErrDigestMismatch
// (callers map it per context).
func (s *Service) VerifyWebBundle(ctx context.Context, ownerUserID, artifactID, expectedDigest string) (BundleSummary, error) {
	if !domain.ValidArtifactDigest(expectedDigest) {
		return BundleSummary{}, domain.ErrInvalid
	}
	artifact, err := s.Get(ctx, ownerUserID, artifactID)
	if err != nil {
		return BundleSummary{}, err
	}
	if artifact.Type != domain.TypeWebBundle {
		return BundleSummary{}, domain.ErrNotFound
	}
	if artifact.Digest != expectedDigest {
		return BundleSummary{}, domain.ErrDigestMismatch
	}
	return BundleSummary{Entrypoint: artifact.Entrypoint}, nil
}

// ReadVerifiedWebBundleAsset reads one normalized file from the artifact only
// after proving it still carries the exact expected digest. The expected
// digest comes from the pinned launch descriptor, so any drift fails closed.
func (s *Service) ReadVerifiedWebBundleAsset(ctx context.Context, ownerUserID, artifactID, expectedDigest, path string) (domain.BundleFile, error) {
	if !domain.ValidArtifactDigest(expectedDigest) {
		return domain.BundleFile{}, errReferenceCorrupt
	}
	if path != "" && !domain.ValidBundleAssetPath(path) {
		return domain.BundleFile{}, domain.ErrNotFound
	}
	artifact, err := s.Get(ctx, ownerUserID, artifactID)
	if err != nil {
		return domain.BundleFile{}, err
	}
	if artifact.Type != domain.TypeWebBundle {
		return domain.BundleFile{}, domain.ErrNotFound
	}
	if artifact.Digest != expectedDigest {
		return domain.BundleFile{}, errReferenceCorrupt
	}
	target := path
	if target == "" {
		target = artifact.Entrypoint
	}
	return s.repository.ReadAsset(ctx, ownerUserID, artifactID, target)
}

// errReferenceCorrupt marks a resolution-time digest drift or malformed
// reference: immutable artifacts cannot drift, so this is an internal
// invariant failure mapped to a sanitized Internal error.
var errReferenceCorrupt = errors.New("referenced bundle artifact does not match its pinned digest")

// IsReferenceCorrupt reports whether err is the sanitized internal
// resolution-corruption verdict. Transport and orchestration map it without
// leaking details.
func IsReferenceCorrupt(err error) bool {
	return errors.Is(err, errReferenceCorrupt)
}

// SanitizeMessage is the fixed, content-free message used for internal
// failures. It never includes SQL, paths, or bundle bytes.
const SanitizeMessage = "artifact operation failed"
