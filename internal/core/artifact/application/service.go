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

// Service executes the artifact use cases for both implemented subtypes:
// bounded web bundle create with durable idempotency, owner-scoped metadata
// reads, single-file reads for the installed-instance resolver, and the
// review subtype's lease-derived materialization preparation, typed content
// reads, and project-scoped listing.
type Service struct {
	repository ports.Repository
	projects   ports.ProjectScope
	ids        ids.Generator
	now        func() time.Time
}

func New(repository ports.Repository, generator ids.Generator) (*Service, error) {
	if repository == nil || generator == nil {
		return nil, errors.New("artifact service requires repository and id generator")
	}
	return &Service{repository: repository, ids: generator, now: func() time.Time { return time.Now().UTC() }}, nil
}

// WithProjectScope attaches the neutral project liveness port that
// project-scoped review reads require. Without it the project list stays
// unsupported and review content reads still work by artifact ID.
func (s *Service) WithProjectScope(scope ports.ProjectScope) (*Service, error) {
	if scope == nil {
		return nil, errors.New("artifact service requires a project scope port")
	}
	s.projects = scope
	return s, nil
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

// List returns one page of the owner's artifacts ordered by ID. An empty
// project filter lists across both implemented subtypes; a project filter
// lists that project's review artifacts after the neutral project scope port
// proves the project exists and belongs to this owner. The page size is
// normalized exactly once here.
func (s *Service) List(ctx context.Context, ownerUserID, projectID, cursor string, pageSize int) (ports.PageResult, error) {
	if ownerUserID == "" {
		return ports.PageResult{}, domain.ErrInvalid
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
	var idsPage []string
	var nextCursor string
	var err error
	if projectID == "" {
		idsPage, nextCursor, err = s.repository.ListIDsPage(ctx, ownerUserID, cursor, pageSize)
	} else {
		if !domain.ValidArtifactUUID(projectID) {
			return ports.PageResult{}, domain.ErrInvalid
		}
		if s.projects == nil {
			// Composition must wire the project scope port for project
			// listing; without it the answer is fail-closed unsupported.
			return ports.PageResult{}, domain.ErrUnsupported
		}
		if scopeErr := s.projects.ValidateReadableProject(ctx, ownerUserID, projectID); scopeErr != nil {
			return ports.PageResult{}, scopeErr
		}
		idsPage, nextCursor, err = s.repository.ListProjectReviewIDsPage(ctx, ownerUserID, projectID, cursor, pageSize)
	}
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

// ReviewOutputRequestDigestFor computes the canonical materialization request
// digest for untrusted raw input without minting or persisting anything. It
// is the exact digest PrepareReviewOutput stores; false means the input
// violates the output grammar and can never match a stored mapping.
func ReviewOutputRequestDigestFor(projectID, taskID, outputKey, rawTitle, artifactType string, rawContent []byte) (string, bool) {
	if !domain.ValidArtifactUUID(projectID) || !domain.ValidArtifactUUID(taskID) ||
		!domain.ValidReviewOutputKey(outputKey) {
		return "", false
	}
	canonicalType, _, ok := domain.ReviewType(artifactType)
	if !ok {
		return "", false
	}
	title, ok := domain.NormalizeReviewTitle(rawTitle)
	if !ok {
		return "", false
	}
	normalized, err := domain.NormalizeReviewContent(canonicalType, rawContent)
	if err != nil {
		return "", false
	}
	return domain.ReviewOutputRequestDigest(projectID, taskID, outputKey, title, normalized.Digest), true
}

// GetReview returns one owner-scoped review artifact's authoritative metadata
// and exact canonical content. The repository serves both from the same row
// snapshot and revalidates the stored fact, so metadata/content mismatch is
// impossible by construction and drift surfaces as sanitized Internal. A web
// bundle ID is not a review artifact: it resolves to the fixed unsupported
// verdict and never to bundle bytes or a NotFound that could double as an
// existence probe.
func (s *Service) GetReview(ctx context.Context, ownerUserID, artifactID string) (domain.ReviewArtifact, domain.NormalizedReviewContent, error) {
	if ownerUserID == "" || !domain.ValidArtifactUUID(artifactID) {
		return domain.ReviewArtifact{}, domain.NormalizedReviewContent{}, domain.ErrInvalid
	}
	fact, content, err := s.repository.GetReviewContent(ctx, ownerUserID, artifactID)
	if errors.Is(err, domain.ErrNotFound) {
		metadata, getErr := s.Get(ctx, ownerUserID, artifactID)
		if getErr != nil {
			return domain.ReviewArtifact{}, domain.NormalizedReviewContent{}, err
		}
		if domain.IsReviewType(metadata.Type) {
			return domain.ReviewArtifact{}, domain.NormalizedReviewContent{}, domain.ErrCorrupt
		}
		return domain.ReviewArtifact{}, domain.NormalizedReviewContent{}, domain.ErrUnsupported
	}
	return fact, content, err
}

// PrepareReviewOutput validates one untrusted provider artifact output and
// builds the exact canonical command the materialization coordinator persists
// inside its transaction: the server-minted artifact identity, the normalized
// content bytes, and the request digest that adjudicates replay versus
// conflict for the (task, output key) identity. This is pure preparation —
// nothing is consumed or persisted, so validation failures never consume the
// output key.
func (s *Service) PrepareReviewOutput(ownerUserID, projectID, taskID, outputKey, rawTitle, artifactType string, rawContent []byte) (ports.ReviewOutputCommand, error) {
	if ownerUserID == "" || !domain.ValidArtifactUUID(projectID) || !domain.ValidArtifactUUID(taskID) ||
		!domain.ValidReviewOutputKey(outputKey) {
		return ports.ReviewOutputCommand{}, domain.ErrInvalid
	}
	canonicalType, mediaType, ok := domain.ReviewType(artifactType)
	if !ok {
		return ports.ReviewOutputCommand{}, domain.ErrInvalid
	}
	title, ok := domain.NormalizeReviewTitle(rawTitle)
	if !ok {
		return ports.ReviewOutputCommand{}, domain.ErrInvalid
	}
	normalized, err := domain.NormalizeReviewContent(canonicalType, rawContent)
	if err != nil {
		return ports.ReviewOutputCommand{}, err
	}
	fact := domain.ReviewArtifact{
		ID: s.ids.New(), OwnerUserID: ownerUserID, ProjectID: projectID,
		SourceTask: taskID, OutputKey: outputKey, Type: canonicalType, Title: title,
		MediaType: mediaType, Digest: normalized.Digest, ByteCount: normalized.ByteCount,
		LineCount: normalized.LineCount, CreatedAt: s.now(),
	}
	return ports.ReviewOutputCommand{
		Artifact:      fact,
		Content:       normalized.Content,
		RequestDigest: domain.ReviewOutputRequestDigest(projectID, taskID, outputKey, title, normalized.Digest),
	}, nil
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
