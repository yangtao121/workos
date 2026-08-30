package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/core/appregistry/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// ErrProjectDenied marks a ListApps project context that the owner cannot
// use: missing, foreign, or archived. Transport maps it to NotFound so it is
// indistinguishable from an unknown project.
var ErrProjectDenied = errors.New("project context is not available")

// ProjectSummary is the neutral project view the registry needs. It is
// intentionally not the Project entity: the registry never imports Project
// adapters or SQL, only this application port implemented by the neutral
// orchestration layer.
type ProjectSummary struct {
	ArchivedAt *time.Time
}

// ProjectDirectory resolves an owner-scoped project for ListApps context
// validation without exposing Project storage to the registry.
type ProjectDirectory interface {
	Get(ctx context.Context, ownerUserID, projectID string) (ProjectSummary, error)
}

// ErrArtifactDenied marks a manifest's web bundle reference that the owner
// cannot use: missing, foreign, or digest-mismatched. Transport maps it to
// NotFound so it is indistinguishable from an unknown artifact.
var ErrArtifactDenied = errors.New("referenced artifact is not available")

// ArtifactDirectory verifies a manifest's web bundle reference against the
// owner's immutable artifacts. It is the neutral port implemented by the
// orchestration layer over the Artifact module; the registry never imports
// artifact adapters or SQL.
type ArtifactDirectory interface {
	VerifyWebBundle(ctx context.Context, ownerUserID, artifactID, artifactDigest string) error
}

// ManifestValidator turns untrusted YAML bytes into either a validated
// manifest or bounded, value-free violations.
type ManifestValidator interface {
	Validate(yamlBytes []byte) (domain.Manifest, []string)
}

// PageResult is the explicit paging contract: items plus the next cursor as
// decided by the repository probe. Transport forwards the token verbatim and
// never recomputes paging from the raw request.
type PageResult struct {
	Items     []domain.AppVersionSummary
	NextToken string
}

const (
	defaultPageSize = 50
	maxPageSize     = 100
)

type Service struct {
	repository ports.Repository
	validator  ManifestValidator
	projects   ProjectDirectory
	artifacts  ArtifactDirectory
	ids        ids.Generator
	now        func() time.Time
}

func New(repository ports.Repository, validator ManifestValidator, projects ProjectDirectory, artifacts ArtifactDirectory, generator ids.Generator) (*Service, error) {
	if repository == nil || validator == nil || generator == nil {
		return nil, errors.New("app registry requires repository, validator, and id generator")
	}
	return &Service{
		repository: repository, validator: validator, projects: projects, artifacts: artifacts,
		ids: generator, now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// ValidateManifest returns the public summary for acceptable manifests and
// bounded violations otherwise. It never persists state.
func (s *Service) ValidateManifest(ctx context.Context, yamlBytes []byte) (domain.Manifest, []string, error) {
	if len(yamlBytes) > manifestLimit() {
		return domain.Manifest{}, []string{fmt.Sprintf("manifest exceeds the %d byte limit", manifestLimit())}, nil
	}
	manifest, violations := s.validator.Validate(yamlBytes)
	return manifest, violations, nil
}

// Register validates and persists one immutable App version for the owner.
// A web-bundle launch descriptor is verified against the owner's immutable
// artifact before anything is persisted: the artifact must exist, belong to
// the same owner, and carry the exact referenced digest.
func (s *Service) Register(ctx context.Context, ownerUserID, idempotencyKey string, yamlBytes []byte) (domain.AppVersionSummary, error) {
	if ownerUserID == "" || !domain.ValidIdempotencyKey(idempotencyKey) || len(yamlBytes) == 0 || len(yamlBytes) > manifestLimit() {
		return domain.AppVersionSummary{}, domain.ErrInvalid
	}
	manifest, violations := s.validator.Validate(yamlBytes)
	if len(violations) > 0 {
		return domain.AppVersionSummary{}, domain.ErrInvalid
	}
	if _, ok := domain.ParseVersion(manifest.Version); !ok {
		return domain.AppVersionSummary{}, domain.ErrInvalid
	}
	if manifest.WebBundle != nil {
		if s.artifacts == nil {
			return domain.AppVersionSummary{}, errors.New("artifact directory is not configured")
		}
		if err := s.artifacts.VerifyWebBundle(ctx, ownerUserID, manifest.WebBundle.ArtifactID, manifest.WebBundle.ArtifactDigest); err != nil {
			if errors.Is(err, ErrArtifactDenied) {
				return domain.AppVersionSummary{}, domain.ErrNotFound
			}
			return domain.AppVersionSummary{}, fmt.Errorf("verify web bundle reference: %w", err)
		}
	}
	record := domain.AppVersion{
		ID: s.ids.New(), OwnerUserID: ownerUserID, AppID: manifest.ID, Version: manifest.Version,
		Scope: manifest.Scope, Name: manifest.Name, Permissions: manifest.Permissions,
		ManifestDigest: manifest.Digest, CanonicalManifest: manifest.CanonicalJSON,
		IdempotencyKey: idempotencyKey, RequestDigest: manifest.Digest, CreatedAt: s.now(),
	}
	return s.repository.Register(ctx, record)
}

// ErrUnsupportedRuntime marks an app version whose manifest has no supported
// web bundle launch descriptor. The surface path maps it to
// FailedPrecondition: the installation is healthy, but not launchable in
// this slice.
var ErrUnsupportedRuntime = errors.New("app version has no supported web bundle runtime")

// WebBundleResolution is the exact launch fact of one immutable version: the
// manifest digest plus the descriptor parsed from the canonical bytes.
type WebBundleResolution struct {
	ManifestDigest string
	Ref            domain.WebBundleRef
}

// ResolveWebBundle resolves the exact immutable version's launch descriptor
// for the installed-instance resolver. It reads the version's canonical
// manifest only; no registry current is involved.
func (s *Service) ResolveWebBundle(ctx context.Context, ownerUserID, appID, version string) (WebBundleResolution, error) {
	if ownerUserID == "" || !domain.ValidAppID(appID) {
		return WebBundleResolution{}, domain.ErrInvalid
	}
	if _, ok := domain.ParseVersion(version); !ok {
		return WebBundleResolution{}, domain.ErrInvalid
	}
	digest, canonical, err := s.repository.GetVersionManifest(ctx, ownerUserID, appID, version)
	if err != nil {
		return WebBundleResolution{}, err
	}
	ref, ok := domain.ParseWebBundleRef(canonical)
	if !ok {
		return WebBundleResolution{}, ErrUnsupportedRuntime
	}
	return WebBundleResolution{ManifestDigest: digest, Ref: ref}, nil
}

// ContainerResolution is the exact launch fact of one immutable container
// version: the manifest digest plus the descriptor parsed from the canonical
// bytes (re-validated at parse time, so a corrupt stored manifest cannot
// resolve).
type ContainerResolution struct {
	ManifestDigest string
	Launch         domain.ContainerLaunch
}

// ResolveContainer resolves the exact immutable version's container launch
// descriptor. A version whose manifest is not a healthy container profile is
// ErrUnsupportedRuntime; a stored manifest that violates its own profile is
// the same verdict (it is never silently defaulted), and the digest-equality
// check against the installation happens in the orchestration resolver.
func (s *Service) ResolveContainer(ctx context.Context, ownerUserID, appID, version string) (ContainerResolution, error) {
	if ownerUserID == "" || !domain.ValidAppID(appID) {
		return ContainerResolution{}, domain.ErrInvalid
	}
	if _, ok := domain.ParseVersion(version); !ok {
		return ContainerResolution{}, domain.ErrInvalid
	}
	digest, canonical, err := s.repository.GetVersionManifest(ctx, ownerUserID, appID, version)
	if err != nil {
		return ContainerResolution{}, err
	}
	launch, ok := domain.ParseContainerLaunch(canonical)
	if !ok {
		return ContainerResolution{}, ErrUnsupportedRuntime
	}
	return ContainerResolution{ManifestDigest: digest, Launch: launch}, nil
}

// SurfaceResolution is the renderer-neutral launch fact of one immutable
// version: the manifest digest plus whichever supported descriptor its
// canonical manifest declares. Exactly one side is set for a supported
// runtime; neither is set for an unsupported one (ErrUnsupportedRuntime).
type SurfaceResolution struct {
	ManifestDigest string
	WebBundle      *domain.WebBundleRef
	Container      *domain.ContainerLaunch
}

// ResolveSurfaceLaunch resolves the exact immutable version's renderer-neutral
// launch facts. The canonical manifest is parsed once and its declared runtime
// profile decides the descriptor; a stored manifest that satisfies no profile
// is ErrUnsupportedRuntime rather than a silently defaulted launch.
func (s *Service) ResolveSurfaceLaunch(ctx context.Context, ownerUserID, appID, version string) (SurfaceResolution, error) {
	if ownerUserID == "" || !domain.ValidAppID(appID) {
		return SurfaceResolution{}, domain.ErrInvalid
	}
	if _, ok := domain.ParseVersion(version); !ok {
		return SurfaceResolution{}, domain.ErrInvalid
	}
	digest, canonical, err := s.repository.GetVersionManifest(ctx, ownerUserID, appID, version)
	if err != nil {
		return SurfaceResolution{}, err
	}
	resolution := SurfaceResolution{ManifestDigest: digest}
	if ref, ok := domain.ParseWebBundleRef(canonical); ok {
		resolution.WebBundle = &ref
		return resolution, nil
	}
	if launch, ok := domain.ParseContainerLaunch(canonical); ok {
		resolution.Container = &launch
		return resolution, nil
	}
	return SurfaceResolution{}, ErrUnsupportedRuntime
}

// Get returns the current version for an empty version, or the exact
// immutable version when one is requested. Both paths read the bounded
// summary projection only.
func (s *Service) Get(ctx context.Context, ownerUserID, appID, version string) (domain.AppVersionSummary, error) {
	if ownerUserID == "" || !domain.ValidAppID(appID) {
		return domain.AppVersionSummary{}, domain.ErrInvalid
	}
	if version != "" {
		if _, ok := domain.ParseVersion(version); !ok {
			return domain.AppVersionSummary{}, domain.ErrInvalid
		}
		return s.repository.GetVersion(ctx, ownerUserID, appID, version)
	}
	current, ok, err := s.currentVersion(ctx, ownerUserID, appID)
	if err != nil {
		return domain.AppVersionSummary{}, err
	}
	if !ok {
		return domain.AppVersionSummary{}, domain.ErrNotFound
	}
	return current, nil
}

// List returns the current version of every registered app, ordered by app
// ID, one page at a time. The page size is normalized exactly once here:
// zero means the default, values above the maximum clamp to it, and negative
// values are rejected. A non-empty projectID first proves the project belongs
// to the owner and is not archived; the result is the owner's registry
// catalog in that project context, never an installation state.
func (s *Service) List(ctx context.Context, ownerUserID, projectID, cursor string, pageSize int) (PageResult, error) {
	if ownerUserID == "" {
		return PageResult{}, domain.ErrInvalid
	}
	switch {
	case pageSize < 0:
		return PageResult{}, domain.ErrInvalid
	case pageSize == 0:
		pageSize = defaultPageSize
	case pageSize > maxPageSize:
		pageSize = maxPageSize
	}
	if cursor != "" && !domain.ValidAppID(cursor) {
		return PageResult{}, domain.ErrInvalid
	}
	if projectID != "" {
		if !domain.ValidUUID(projectID) {
			return PageResult{}, domain.ErrInvalid
		}
		if s.projects == nil {
			return PageResult{}, errors.New("project directory is not configured")
		}
		if _, err := s.projects.Get(ctx, ownerUserID, projectID); err != nil {
			if errors.Is(err, ErrProjectDenied) {
				return PageResult{}, domain.ErrNotFound
			}
			return PageResult{}, fmt.Errorf("resolve project context: %w", err)
		}
	}
	appIDs, nextCursor, err := s.repository.ListAppIDPage(ctx, ownerUserID, cursor, pageSize)
	if err != nil {
		return PageResult{}, err
	}
	if len(appIDs) == 0 {
		return PageResult{Items: []domain.AppVersionSummary{}, NextToken: ""}, nil
	}
	currents, err := s.currentVersions(ctx, ownerUserID, appIDs)
	if err != nil {
		return PageResult{}, err
	}
	return PageResult{Items: currents, NextToken: nextCursor}, nil
}

// currentVersion folds the streamed summaries of one app into its SemVer
// current version using a single-candidate accumulator.
func (s *Service) currentVersion(ctx context.Context, ownerUserID, appID string) (domain.AppVersionSummary, bool, error) {
	currents, err := s.currentVersions(ctx, ownerUserID, []string{appID})
	if err != nil {
		return domain.AppVersionSummary{}, false, err
	}
	if len(currents) == 0 {
		return domain.AppVersionSummary{}, false, nil
	}
	return currents[0], true, nil
}

// currentVersions folds the repository's app-ID-ordered summary stream into
// one current version per app. Memory is bounded by the accumulator (one
// candidate) plus the result slice (one entry per requested app).
func (s *Service) currentVersions(ctx context.Context, ownerUserID string, appIDs []string) ([]domain.AppVersionSummary, error) {
	currents := make([]domain.AppVersionSummary, 0, len(appIDs))
	var (
		candidate      domain.AppVersionSummary
		candidateParse domain.Version
		haveCandidate  bool
	)
	err := s.repository.VisitVersionSummaries(ctx, ownerUserID, appIDs, func(summary domain.AppVersionSummary) error {
		parsed, ok := domain.ParseVersion(summary.Version)
		if !ok {
			// Every stored version passed the validator; an unparseable one is
			// a corrupted invariant, surfaced as a sanitized internal error.
			return errStoredVersionCorrupt
		}
		if !haveCandidate || candidate.AppID != summary.AppID {
			if haveCandidate {
				currents = append(currents, candidate)
			}
			candidate, candidateParse, haveCandidate = summary, parsed, true
			return nil
		}
		if domain.CompareVersion(parsed, candidateParse) > 0 {
			candidate, candidateParse = summary, parsed
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if haveCandidate {
		currents = append(currents, candidate)
	}
	return currents, nil
}

// errStoredVersionCorrupt has no domain conflict semantics, so transport maps
// it to a sanitized Internal error.
var errStoredVersionCorrupt = errors.New("stored app version is not parseable")

func manifestLimit() int {
	return domain.MaxManifestBytes
}
