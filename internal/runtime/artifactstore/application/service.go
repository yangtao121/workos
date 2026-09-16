// Package application drives the release bundle repository (ADR-0033):
// operator import, build-output freeze commits, verification queries, and
// startup reconciliation. Files and metadata converge without distributed
// transactions: bytes are promoted durably before the ready row is written,
// and reconciliation is the truth-maker after crashes.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
	"github.com/yangtao121/workos/internal/runtime/artifactstore/ports"
)

// Service implements the artifact repository use cases.
type Service struct {
	meta  ports.MetadataStore
	files ports.BundleFiles
	now   func() time.Time
}

func New(meta ports.MetadataStore, files ports.BundleFiles) *Service {
	return &Service{meta: meta, files: files, now: time.Now}
}

// ImportRequest is one operator import (ADR-0033 section 5). Reader is the
// complete bundle byte stream; the server never trusts a claimed digest.
type ImportRequest struct {
	OwnerUserID     string
	AppID           string
	IdempotencyKey  string
	ExpectedDigest  string
	Reader          io.Reader
}

// Result reports the durable outcome. Created=false means an identical
// artifact already existed (same key or same content).
type Result struct {
	Artifact domain.Artifact
	Created  bool
}

func (s *Service) nowUTC() time.Time { return s.now().UTC() }

// Import validates, freezes, and records one operator import. Same key with
// the same content replays the stored artifact; same key with different
// content is a stable conflict. Every stream is bounded and structurally
// verified before anything becomes ready.
func (s *Service) Import(ctx context.Context, req ImportRequest) (Result, error) {
	if !domain.ValidUUID(req.OwnerUserID) {
		return Result{}, fmt.Errorf("%w: owner is not a UUIDv7", domain.ErrInvalidRequest)
	}
	if !domain.ValidAppID(req.AppID) {
		return Result{}, fmt.Errorf("%w: app id malformed", domain.ErrInvalidRequest)
	}
	if req.IdempotencyKey == "" || len(req.IdempotencyKey) > 128 {
		return Result{}, fmt.Errorf("%w: idempotency key must be 1-128 bytes", domain.ErrInvalidRequest)
	}
	if req.ExpectedDigest != "" && !domain.ValidDigest(req.ExpectedDigest) {
		return Result{}, fmt.Errorf("%w: expected digest malformed", domain.ErrInvalidRequest)
	}
	// An earlier import under the same key pins what this key means: the
	// incoming bytes are always measured and compared against it. The lookup
	// is advisory only; identity comes from the recomputed digest.
	existingByKey, keyErr := s.meta.GetByImportKey(ctx, req.OwnerUserID, req.IdempotencyKey)
	if keyErr != nil && !errors.Is(keyErr, domain.ErrNotFound) {
		return Result{}, keyErr
	}
	if err := s.checkQuota(ctx, req.OwnerUserID, domain.MaxEncodedBundleBytes); err != nil {
		return Result{}, err
	}

	staging, err := s.files.TempFile(req.OwnerUserID)
	if err != nil {
		return Result{}, err
	}
	committed := false
	defer func() {
		if !committed {
			staging.Discard()
		}
	}()
	digest, size, err := writeBounded(staging, req.Reader)
	if err != nil {
		return Result{}, err
	}
	if req.ExpectedDigest != "" && digest != req.ExpectedDigest {
		return Result{}, domain.ErrConflict
	}
	verified, err := verifyStaging(staging, digest)
	if err != nil {
		return Result{}, err
	}
	if keyErr == nil {
		// Same key: identical content replays, different content conflicts
		// (ADR-0033 section 5). The staging copy is dropped either way.
		if existingByKey.Origin != domain.OriginOperatorImport || existingByKey.Digest != digest {
			return Result{}, domain.ErrConflict
		}
		if existingByKey.State != domain.StateReady {
			return Result{}, domain.ErrUnavailable
		}
		if _, _, openErr := s.files.OpenVerified(req.OwnerUserID, existingByKey.Digest); openErr != nil {
			return Result{}, domain.ErrUnavailable
		}
		return Result{Artifact: existingByKey}, nil
	}
	if _, err := staging.Finish(); err != nil {
		return Result{}, err
	}
	if err := s.files.PromoteFile(req.OwnerUserID, digest, staging); err != nil {
		return Result{}, err
	}
	committed = true

	now := s.nowUTC()
	artifact := domain.Artifact{
		ID:              ids.UUIDv7{}.New(),
		OwnerUserID:     req.OwnerUserID,
		Digest:          digest,
		Format:          domain.BundleFormat,
		SizeBytes:       size,
		FileCount:       int32(verified.FileCount),
		State:           domain.StateReady,
		Origin:          domain.OriginOperatorImport,
		IdempotencyKey:  req.IdempotencyKey,
		AppID:           req.AppID,
		CreatedAt:       now,
		ReadyAt:         &now,
		UpdatedAt:       now,
	}
	stored, inserted, err := s.meta.InsertReady(ctx, artifact)
	if err != nil {
		return Result{}, err
	}
	if !inserted {
		// Same (owner, digest) already recorded: identical content. Verify
		// the recorded row is compatible before replaying it.
		if stored.Origin != domain.OriginOperatorImport || stored.State != domain.StateReady {
			return Result{}, domain.ErrConflict
		}
		return Result{Artifact: stored}, nil
	}
	return Result{Artifact: artifact, Created: true}, nil
}

// BuildCommit freezes a real build output directory into the repository with
// build provenance. Called by the Build/Test service only after a fully
// successful build+test verdict under a live lease (C03).
type BuildCommit struct {
	OwnerUserID     string
	TaskID          string
	JobID           string
	IncidentID      string
	ProjectID       string
	InstallationID  string
	SourceBundleID  string
	SourceDigest    string
	ManifestDigest  string
	BaseImage       string
	BuildCommand    []string
	TestCommand     []string
	OutputDirectory string
	IdempotencyKey  string
	OutputDir       string // the real directory to freeze
}

func (s *Service) CommitBuild(ctx context.Context, commit BuildCommit) (Result, error) {
	if !domain.ValidUUID(commit.OwnerUserID) {
		return Result{}, fmt.Errorf("%w: owner is not a UUIDv7", domain.ErrInvalidRequest)
	}
	if commit.IdempotencyKey == "" || len(commit.IdempotencyKey) > 128 {
		return Result{}, fmt.Errorf("%w: idempotency key must be 1-128 bytes", domain.ErrInvalidRequest)
	}
	if info, err := os.Lstat(commit.OutputDir); err != nil || !info.IsDir() {
		return Result{}, fmt.Errorf("%w: output directory missing", domain.ErrInvalidRequest)
	}
	if err := s.checkQuota(ctx, commit.OwnerUserID, domain.MaxEncodedBundleBytes); err != nil {
		return Result{}, err
	}

	staging, err := s.files.TempFile(commit.OwnerUserID)
	if err != nil {
		return Result{}, err
	}
	committed := false
	defer func() {
		if !committed {
			staging.Discard()
		}
	}()
	stats, err := domain.EncodeDirectory(commit.OutputDir, staging)
	if err != nil {
		return Result{}, err
	}
	if _, err := verifyStaging(staging, stats.Digest); err != nil {
		return Result{}, err
	}
	if _, err := staging.Finish(); err != nil {
		return Result{}, err
	}
	if err := s.files.PromoteFile(commit.OwnerUserID, stats.Digest, staging); err != nil {
		return Result{}, err
	}
	committed = true

	now := s.nowUTC()
	artifact := domain.Artifact{
		ID:              ids.UUIDv7{}.New(),
		OwnerUserID:     commit.OwnerUserID,
		Digest:          stats.Digest,
		Format:          domain.BundleFormat,
		SizeBytes:       stats.EncodedSize,
		FileCount:       int32(stats.FileCount),
		State:           domain.StateReady,
		Origin:          domain.OriginBuildJob,
		IdempotencyKey:  commit.IdempotencyKey,
		TaskID:          commit.TaskID,
		JobID:           commit.JobID,
		IncidentID:      commit.IncidentID,
		ProjectID:       commit.ProjectID,
		InstallationID:  commit.InstallationID,
		SourceBundleID:  commit.SourceBundleID,
		SourceDigest:    commit.SourceDigest,
		ManifestDigest:  commit.ManifestDigest,
		BaseImage:       commit.BaseImage,
		BuildCommand:    commit.BuildCommand,
		TestCommand:     commit.TestCommand,
		OutputDirectory: commit.OutputDirectory,
		CreatedAt:       now,
		ReadyAt:         &now,
		UpdatedAt:       now,
	}
	stored, inserted, err := s.meta.InsertReady(ctx, artifact)
	if err != nil {
		return Result{}, err
	}
	if !inserted {
		if stored.Origin != domain.OriginBuildJob || stored.Digest != stats.Digest {
			return Result{}, domain.ErrConflict
		}
		return Result{Artifact: stored}, nil
	}
	return Result{Artifact: artifact, Created: true}, nil
}

// FactsQuery locates one artifact by task or id.
type FactsQuery struct {
	TaskID     string
	ArtifactID string
}

// Facts returns the authoritative metadata for Core/Reliability verification.
func (s *Service) Facts(ctx context.Context, query FactsQuery) (domain.Artifact, error) {
	switch {
	case query.ArtifactID != "":
		return s.meta.GetByID(ctx, query.ArtifactID)
	case query.TaskID != "":
		return s.meta.GetByTask(ctx, query.TaskID)
	default:
		return domain.Artifact{}, fmt.Errorf("%w: task id or artifact id required", domain.ErrInvalidRequest)
	}
}

// VerifyReady fully re-hashes the stored bundle bytes: the launch-side gate.
func (s *Service) VerifyReady(ctx context.Context, owner, digest string) error {
	if _, _, err := s.files.OpenVerified(owner, digest); err != nil {
		return err
	}
	return nil
}

// OpenForLaunch returns the verified on-disk bundle path for the workload
// engine. The digest is re-hashed on every open; a mismatch is fatal.
func (s *Service) OpenForLaunch(ctx context.Context, owner, digest string) (string, error) {
	path, _, err := s.files.OpenVerified(owner, digest)
	return path, err
}

// Reconcile converges metadata and bytes after a crash (ADR-0033 section 6):
// staging files are removed first, then preparing rows settle by measured
// digest, and ready rows whose bytes vanished degrade to unavailable.
func (s *Service) Reconcile(ctx context.Context) error {
	if err := s.files.CleanTemp(); err != nil {
		return err
	}
	preparing, err := s.meta.ListByState(ctx, domain.StatePreparing)
	if err != nil {
		return err
	}
	for _, artifact := range preparing {
		if !s.files.Has(artifact.OwnerUserID, artifact.Digest) {
			if err := s.meta.MarkState(ctx, artifact.ID, domain.StateFailed); err != nil {
				return err
			}
			continue
		}
		if _, _, openErr := s.files.OpenVerified(artifact.OwnerUserID, artifact.Digest); openErr != nil {
			if err := s.meta.MarkState(ctx, artifact.ID, domain.StateFailed); err != nil {
				return err
			}
			continue
		}
		if err := s.meta.MarkState(ctx, artifact.ID, domain.StateReady); err != nil {
			return err
		}
	}
	ready, err := s.meta.ListByState(ctx, domain.StateReady)
	if err != nil {
		return err
	}
	for _, artifact := range ready {
		if !s.files.Has(artifact.OwnerUserID, artifact.Digest) {
			if err := s.meta.MarkState(ctx, artifact.ID, domain.StateUnavailable); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) checkQuota(ctx context.Context, owner string, incoming int64) error {
	usage, err := s.meta.OwnerUsageBytes(ctx, owner)
	if err != nil {
		return err
	}
	if usage+incoming > domain.OwnerQuotaBytes {
		return fmt.Errorf("%w: owner usage %d + %d exceeds %d", domain.ErrQuotaExceeded, usage, incoming, domain.OwnerQuotaBytes)
	}
	return nil
}

// writeBounded copies the stream while hashing, rejecting any byte past the
// encoded-bundle cap so oversized imports fail mid-stream, not at the end.
func writeBounded(w io.Writer, r io.Reader) (string, int64, error) {
	digest := sha256.New()
	var total int64
	buf := make([]byte, 64<<10)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > domain.MaxEncodedBundleBytes {
				return "", 0, fmt.Errorf("%w: stream exceeds %d bytes", domain.ErrBundleTooLarge, domain.MaxEncodedBundleBytes)
			}
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return "", 0, writeErr
			}
			if _, hashErr := digest.Write(buf[:n]); hashErr != nil {
				return "", 0, hashErr
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
	if total == 0 {
		return "", 0, fmt.Errorf("%w: empty bundle", domain.ErrBundleInvalid)
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), total, nil
}

// verifyStaging structurally validates the finished staging file and leaves
// it positioned for promotion.
func verifyStaging(staging ports.StagingFile, digest string) (domain.Stats, error) {
	file, err := os.Open(staging.Name())
	if err != nil {
		return domain.Stats{}, err
	}
	stats, verifyErr := domain.Verify(file, digest)
	closeErr := file.Close()
	if verifyErr != nil {
		return domain.Stats{}, verifyErr
	}
	return stats, closeErr
}
