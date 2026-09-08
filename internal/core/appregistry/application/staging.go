package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/core/appregistry/ports"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"github.com/yangtao121/workos/internal/platform/ids"
)

var (
	ErrStagingUnavailable = errors.New("app staging requires the staging store")
	ErrNotStaged          = errors.New("app version is not staged")
)

// StagingRegistration is the verified build verdict Reliability presents.
// Core re-derives every fact; nothing here trusts the caller's summary.
type StagingRegistration struct {
	OwnerUserID    string
	TaskID         string
	IncidentID     string
	ProjectID      string
	InstallationID string
	BuildJobID     string
	SourceDigest   string
	// The verified original version facts resolved from the task's pinned
	// repair target (ADR-0022 snapshot).
	AppID              string
	BaseVersion        string
	BaseManifestDigest string
}

type StagingResult struct {
	Version        string
	ManifestDigest string
	Created        bool
}

type StagingService struct {
	store     ports.StagingStore
	builds    *BuildService
	validator ManifestValidator
	ids       ids.Generator
}

func NewStagingService(store ports.StagingStore, builds *BuildService, validator ManifestValidator, generator ids.Generator) (*StagingService, error) {
	if store == nil || builds == nil || validator == nil || generator == nil {
		return nil, errors.New("app staging requires store, builds, validator and ids")
	}
	return &StagingService{store: store, builds: builds, validator: validator, ids: generator}, nil
}

// Register derives and persists the staged candidate version for one
// completed repair task. The new manifest is the original version's
// canonical manifest with exactly the build source binding replaced; every
// other byte is preserved. Idempotent per task: the first result replays.
func (s *StagingService) Register(ctx context.Context, tx dbtx.Tx, registration StagingRegistration) (StagingResult, error) {
	if err := validateStagingRegistration(registration); err != nil {
		return StagingResult{}, err
	}
	// Replay-first: a task can stage exactly one version, forever.
	mapping, err := s.store.FindCandidateVersion(ctx, tx, registration.TaskID)
	switch {
	case err == nil:
		version, err := s.store.GetVersionAnyState(ctx, tx, mapping.AppVersionID)
		if err != nil {
			return StagingResult{}, err
		}
		if version.AppID == "" || mapping.SourceDigest != registration.SourceDigest {
			return StagingResult{}, domain.ErrSourceCorrupt
		}
		return StagingResult{Version: version.Version, ManifestDigest: version.ManifestDigest, Created: false}, nil
	case errors.Is(err, domain.ErrNotFound):
	default:
		return StagingResult{}, err
	}
	// The candidate source must exist and match the presented digest.
	build, err := s.builds.Resolve(ctx, tx, registration.OwnerUserID, registration.AppID, registration.BaseVersion, registration.BaseManifestDigest)
	if err != nil {
		return StagingResult{}, err
	}
	baseDigest, baseRaw, err := s.builds.Manifest(ctx, tx, registration.OwnerUserID, registration.AppID, registration.BaseVersion)
	if err != nil {
		return StagingResult{}, err
	}
	if baseDigest != registration.BaseManifestDigest || len(baseRaw) == 0 {
		return StagingResult{}, domain.ErrSourceCorrupt
	}
	candidate, err := s.builds.Candidate(ctx, tx, registration.OwnerUserID, registration.TaskID)
	if err != nil {
		return StagingResult{}, err
	}
	if candidate.OwnerUserID != registration.OwnerUserID || candidate.Digest != registration.SourceDigest || candidate.ID != build.Source.ID {
		return StagingResult{}, domain.ErrSourceCorrupt
	}
	derived, digest, err := deriveStagedManifest(baseRaw, candidate, registration.BaseVersion, registration.TaskID)
	if err != nil {
		return StagingResult{}, err
	}
	manifest, violations := s.validator.Validate(derived)
	if len(violations) > 0 || manifest.Digest != digest || manifest.Build == nil ||
		manifest.Build.SourceBundleID != candidate.ID || manifest.Build.SourceDigest != candidate.Digest {
		return StagingResult{}, domain.ErrSourceCorrupt
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	version := domain.AppVersion{
		ID: s.ids.New(), OwnerUserID: registration.OwnerUserID, AppID: registration.AppID,
		Version: manifest.Version, Scope: manifest.Scope, Name: manifest.Name,
		Permissions: manifest.Permissions, ManifestDigest: manifest.Digest,
		CanonicalManifest: manifest.CanonicalJSON, CreatedAt: now,
	}
	versionID, err := s.store.InsertStagedVersion(ctx, tx, version, now)
	if err != nil {
		// A concurrent registration of the same task may have won the race;
		// replay it instead of surfacing a spurious conflict.
		if errors.Is(err, domain.ErrIdempotencyConflict) {
			replay, replayErr := s.store.FindCandidateVersion(ctx, tx, registration.TaskID)
			if replayErr == nil && replay.TaskID == registration.TaskID {
				stored, storedErr := s.store.GetVersionAnyState(ctx, tx, replay.AppVersionID)
				if storedErr == nil {
					return StagingResult{Version: stored.Version, ManifestDigest: stored.ManifestDigest, Created: false}, nil
				}
			}
		}
		return StagingResult{}, err
	}
	mapping = ports.CandidateVersionMapping{
		TaskID: registration.TaskID, OwnerUserID: registration.OwnerUserID,
		ProjectID: registration.ProjectID, InstallationID: registration.InstallationID,
		IncidentID: registration.IncidentID, BuildJobID: registration.BuildJobID,
		SourceDigest: registration.SourceDigest, AppVersionID: versionID,
	}
	if err := s.store.InsertCandidateVersion(ctx, tx, mapping, now); err != nil {
		return StagingResult{}, err
	}
	return StagingResult{Version: manifest.Version, ManifestDigest: manifest.Digest, Created: true}, nil
}

// Publish flips one task's staged version to published after the canary
// window. Already published replays as a no-op; a missing mapping is NotFound.
func (s *StagingService) Publish(ctx context.Context, tx dbtx.Tx, owner, taskID, version, manifestDigest string) (bool, error) {
	if !domain.ValidSourceID(owner) || !domain.ValidSourceID(taskID) {
		return false, domain.ErrInvalid
	}
	mapping, err := s.store.FindCandidateVersion(ctx, tx, taskID)
	if err != nil {
		return false, err
	}
	if mapping.OwnerUserID != owner {
		return false, domain.ErrNotFound
	}
	stored, err := s.store.GetVersionAnyState(ctx, tx, mapping.AppVersionID)
	if err != nil {
		return false, err
	}
	if stored.Version != version || stored.ManifestDigest != manifestDigest {
		return false, domain.ErrSourceCorrupt
	}
	if stored.State == "published" {
		return false, nil
	}
	if stored.State != "staged" {
		return false, ErrNotStaged
	}
	published, err := s.store.PublishStagedVersion(ctx, tx, taskID, mapping.AppVersionID, time.Now().UTC().Truncate(time.Microsecond))
	if err != nil {
		return false, err
	}
	return published, nil
}

// Staged resolves the staged facts for one task; used by the canary
// transition path to re-verify the exact staged identity.
func (s *StagingService) Staged(ctx context.Context, tx dbtx.Tx, owner, taskID string) (ports.CandidateVersionMapping, ports.StagedVersion, error) {
	mapping, err := s.store.FindCandidateVersion(ctx, tx, taskID)
	if err != nil {
		return ports.CandidateVersionMapping{}, ports.StagedVersion{}, err
	}
	if mapping.OwnerUserID != owner {
		return ports.CandidateVersionMapping{}, ports.StagedVersion{}, domain.ErrNotFound
	}
	staged, err := s.store.GetVersionAnyState(ctx, tx, mapping.AppVersionID)
	if err != nil {
		return ports.CandidateVersionMapping{}, ports.StagedVersion{}, err
	}
	return mapping, staged, nil
}

func validateStagingRegistration(registration StagingRegistration) error {
	for _, id := range []string{registration.OwnerUserID, registration.TaskID, registration.IncidentID, registration.ProjectID, registration.InstallationID, registration.BuildJobID} {
		if !domain.ValidSourceID(id) {
			return domain.ErrInvalid
		}
	}
	if !domain.ValidAppID(registration.AppID) || !domain.ValidWebBundleArtifactDigest(registration.BaseManifestDigest) || !domain.ValidWebBundleArtifactDigest(registration.SourceDigest) {
		return domain.ErrInvalid
	}
	if _, ok := domain.ParseVersion(registration.BaseVersion); !ok {
		return domain.ErrInvalid
	}
	return nil
}

// deriveStagedManifest copies the original canonical manifest onto the
// candidate source binding and derives the deterministic candidate version
// label: {patch+1}-repair.{first 8 hex of the task id}. Every other field is
// preserved byte-for-byte from the original manifest; only the version and
// the two build source keys change.
func deriveStagedManifest(baseRaw []byte, candidate domain.SourceBundle, baseVersion, taskID string) ([]byte, string, error) {
	parsed, ok := domain.ParseVersion(baseVersion)
	if !ok {
		return nil, "", domain.ErrInvalid
	}
	var document map[string]any
	if err := json.Unmarshal(baseRaw, &document); err != nil {
		return nil, "", domain.ErrSourceCorrupt
	}
	label := fmt.Sprintf("%d.%d.%d-repair.%s", parsed.Major, parsed.Minor, parsed.Patch+1, strings.ReplaceAll(taskID, "-", "")[:8])
	if _, ok := domain.ParseVersion(label); !ok {
		return nil, "", domain.ErrInvalid
	}
	document["version"] = label
	build, ok := document["build"].(map[string]any)
	if !ok {
		return nil, "", domain.ErrSourceCorrupt
	}
	build["sourceBundleId"] = candidate.ID
	build["sourceDigest"] = candidate.Digest
	derived, err := domain.CanonicalJSON(document)
	if err != nil {
		return nil, "", domain.ErrSourceCorrupt
	}
	return derived, domain.ManifestDigest(derived), nil
}
