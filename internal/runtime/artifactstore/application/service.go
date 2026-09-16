// Package application drives the release bundle repository (ADR-0033):
// operator import, build-output freeze commits, verification queries, and
// startup reconciliation. Files and metadata converge without distributed
// transactions: bytes are durable before metadata; build readiness additionally
// requires the exact producing job to have committed a success verdict.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/yangtao121/workos/internal/platform/appbundle"
	"github.com/yangtao121/workos/internal/platform/bundleformat"
	"io"
	"os"
	"slices"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
	"github.com/yangtao121/workos/internal/runtime/artifactstore/ports"
)

// Service implements the artifact repository use cases.
type Service struct {
	meta       ports.MetadataStore
	files      ports.BundleFiles
	now        func() time.Time
	buildReady func(context.Context, domain.Artifact) (bool, error)
}

func New(meta ports.MetadataStore, files ports.BundleFiles) *Service {
	return &Service{meta: meta, files: files, now: time.Now}
}

// WithBuildAuthority binds readiness to the producing job's durable terminal verdict.
// Build outputs remain preparing until that job pins this exact immutable artifact.
func (s *Service) WithBuildAuthority(check func(context.Context, domain.Artifact) (bool, error)) *Service {
	s.buildReady = check
	return s
}

// ImportRequest is one operator import (ADR-0033 section 5). Reader is the
// complete bundle byte stream; the server never trusts a claimed digest.
type ImportRequest struct {
	OwnerUserID    string
	AppID          string
	IdempotencyKey string
	ExpectedDigest string
	Reader         io.Reader
}

// Result reports the durable outcome. Created=false means an identical
// artifact already existed under the same task or import key.
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
	unlock, err := s.files.LockOwner(ctx, req.OwnerUserID)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	// An earlier import under the same key pins what this key means: the
	// incoming bytes are always measured and compared against it. The lookup
	// is advisory only; identity comes from the recomputed digest.
	existingByKey, keyErr := s.meta.GetByImportKey(ctx, req.OwnerUserID, req.IdempotencyKey)
	if keyErr != nil && !errors.Is(keyErr, domain.ErrNotFound) {
		return Result{}, keyErr
	}
	if keyErr == nil {
		if existingByKey.AppID != req.AppID || existingByKey.Origin != domain.OriginOperatorImport {
			return Result{}, domain.ErrConflict
		}
		stats, err := appbundle.Verify(req.Reader, "")
		if err != nil {
			return Result{}, err
		}
		if stats.Digest != existingByKey.Digest || (req.ExpectedDigest != "" && stats.Digest != req.ExpectedDigest) {
			return Result{}, domain.ErrConflict
		}
		if existingByKey.State != domain.StateReady {
			return Result{}, domain.ErrUnavailable
		}
		if _, _, err := s.files.OpenVerified(req.OwnerUserID, stats.Digest); err != nil {
			return Result{}, err
		}
		return Result{Artifact: existingByKey}, nil
	}
	if err := s.checkQuota(ctx, req.OwnerUserID, bundleformat.MaxEncodedBundleBytes); err != nil {
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
	if _, err := staging.Finish(); err != nil {
		return Result{}, err
	}
	if err := s.files.PromoteFile(req.OwnerUserID, digest, staging); err != nil {
		return Result{}, err
	}
	committed = true

	now := s.nowUTC()
	artifact := domain.Artifact{
		ID:             ids.UUIDv7{}.New(),
		OwnerUserID:    req.OwnerUserID,
		Digest:         digest,
		Format:         bundleformat.BundleFormat,
		SizeBytes:      size,
		FileCount:      int32(verified.FileCount),
		State:          domain.StateReady,
		Origin:         domain.OriginOperatorImport,
		IdempotencyKey: req.IdempotencyKey,
		AppID:          req.AppID,
		CreatedAt:      now,
		ReadyAt:        &now,
		UpdatedAt:      now,
	}
	stored, inserted, err := s.meta.InsertReady(ctx, artifact)
	if err != nil {
		return Result{}, err
	}
	if !inserted {
		// The import key already has a row. Verify its provenance and
		// bytes before replaying it.
		if stored.Origin != domain.OriginOperatorImport || stored.AppID != req.AppID || stored.Digest != digest || stored.State != domain.StateReady {
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
	AppID           string
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
	unlock, err := s.files.LockOwner(ctx, commit.OwnerUserID)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	// Replays must bind bytes and the complete provenance, including across origins.
	measured, err := appbundle.EncodeDirectory(commit.OutputDir, io.Discard)
	if err != nil {
		return Result{}, err
	}
	existing, taskErr := s.meta.GetByTask(ctx, commit.TaskID)
	if taskErr != nil && !errors.Is(taskErr, domain.ErrNotFound) {
		return Result{}, taskErr
	}
	if taskErr == nil {
		if !sameBuild(existing, commit, measured.Digest) {
			return Result{}, domain.ErrConflict
		}
		if existing.State != domain.StatePreparing && existing.State != domain.StateReady {
			return Result{}, domain.ErrUnavailable
		}
		if _, _, err := s.files.OpenVerified(existing.OwnerUserID, existing.Digest); err != nil {
			return Result{}, err
		}
		return Result{Artifact: existing}, nil
	}
	if err := s.checkQuota(ctx, commit.OwnerUserID, measured.EncodedSize); err != nil {
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
	stats, err := appbundle.EncodeDirectory(commit.OutputDir, staging)
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
		Format:          bundleformat.BundleFormat,
		SizeBytes:       stats.EncodedSize,
		FileCount:       int32(stats.FileCount),
		State:           domain.StatePreparing,
		AppID:           commit.AppID,
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
		ReadyAt:         nil,
		UpdatedAt:       now,
	}
	stored, inserted, err := s.meta.InsertReady(ctx, artifact)
	if err != nil {
		return Result{}, err
	}
	if !inserted {
		if !sameBuild(stored, commit, stats.Digest) {
			return Result{}, domain.ErrConflict
		}
		return Result{Artifact: stored}, nil
	}
	return Result{Artifact: artifact, Created: true}, nil
}

func sameBuild(a domain.Artifact, c BuildCommit, digest string) bool {
	return a.Origin == domain.OriginBuildJob && a.OwnerUserID == c.OwnerUserID && a.AppID == c.AppID &&
		a.TaskID == c.TaskID && a.JobID == c.JobID && a.IncidentID == c.IncidentID &&
		a.ProjectID == c.ProjectID && a.InstallationID == c.InstallationID && a.SourceBundleID == c.SourceBundleID &&
		a.Digest == digest && a.SourceDigest == c.SourceDigest && a.ManifestDigest == c.ManifestDigest &&
		a.BaseImage == c.BaseImage && a.OutputDirectory == c.OutputDirectory && a.IdempotencyKey == c.IdempotencyKey &&
		slices.Equal(a.BuildCommand, c.BuildCommand) && slices.Equal(a.TestCommand, c.TestCommand)
}

// FactsQuery locates one artifact by task or id.
type FactsQuery struct {
	TaskID     string
	ArtifactID string
}

// Facts returns the authoritative metadata for Core/Reliability verification.
// A ready row whose bytes are gone is degraded to unavailable before the
// reply: callers must never treat a vanished bundle as a launchable package.
func (s *Service) Facts(ctx context.Context, query FactsQuery) (domain.Artifact, error) {
	var artifact domain.Artifact
	var err error
	switch {
	case query.ArtifactID != "":
		artifact, err = s.meta.GetByID(ctx, query.ArtifactID)
	case query.TaskID != "":
		artifact, err = s.meta.GetByTask(ctx, query.TaskID)
	default:
		return domain.Artifact{}, fmt.Errorf("%w: task id or artifact id required", domain.ErrInvalidRequest)
	}
	if err != nil {
		return domain.Artifact{}, err
	}
	if query.TaskID != "" && artifact.TaskID != query.TaskID {
		return domain.Artifact{}, domain.ErrConflict
	}
	if artifact.Origin == domain.OriginBuildJob {
		if s.buildReady == nil {
			artifact.State = domain.StatePreparing
			return artifact, nil
		}
		ready, err := s.buildReady(ctx, artifact)
		if err != nil {
			return domain.Artifact{}, err
		}
		if !ready {
			artifact.State = domain.StatePreparing
			return artifact, nil
		}
		if artifact.State == domain.StatePreparing {
			if _, _, err := s.files.OpenVerified(artifact.OwnerUserID, artifact.Digest); err != nil {
				return domain.Artifact{}, err
			}
			if err := s.meta.MarkState(ctx, artifact.ID, domain.StateReady); err != nil {
				return domain.Artifact{}, err
			}
			artifact.State = domain.StateReady
		}
	}
	if artifact.State == domain.StateReady {
		if _, _, openErr := s.files.OpenVerified(artifact.OwnerUserID, artifact.Digest); openErr != nil {
			if markErr := s.meta.MarkState(ctx, artifact.ID, domain.StateUnavailable); markErr != nil {
				return domain.Artifact{}, markErr
			}
			artifact.State = domain.StateUnavailable
		}
	}
	return artifact, nil
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
// staging files are removed under owner locks; build rows need a durable
// success verdict and matching bytes. Missing ready bytes become unavailable.
func (s *Service) Reconcile(ctx context.Context) error {
	if err := s.files.CleanTemp(); err != nil {
		return err
	}
	preparing, err := s.meta.ListByState(ctx, domain.StatePreparing)
	if err != nil {
		return err
	}
	for _, artifact := range preparing {
		if artifact.Origin == domain.OriginBuildJob {
			if _, err := s.Facts(ctx, FactsQuery{ArtifactID: artifact.ID}); err != nil {
				return err
			}
			continue
		}
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
	usage, err := s.files.UsageBytes(owner)
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
			if total > bundleformat.MaxEncodedBundleBytes {
				return "", 0, fmt.Errorf("%w: stream exceeds %d bytes", bundleformat.ErrBundleTooLarge, bundleformat.MaxEncodedBundleBytes)
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
		return "", 0, fmt.Errorf("%w: empty bundle", bundleformat.ErrBundleInvalid)
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), total, nil
}

// verifyStaging structurally validates the finished staging file and leaves
// it positioned for promotion.
func verifyStaging(staging ports.StagingFile, digest string) (bundleformat.Stats, error) {
	file, err := os.Open(staging.Name())
	if err != nil {
		return bundleformat.Stats{}, err
	}
	stats, verifyErr := appbundle.Verify(file, digest)
	closeErr := file.Close()
	if verifyErr != nil {
		return bundleformat.Stats{}, verifyErr
	}
	return stats, closeErr
}
