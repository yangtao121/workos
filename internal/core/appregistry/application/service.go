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

// ManifestValidator turns untrusted YAML bytes into either a validated
// manifest or bounded, value-free violations.
type ManifestValidator interface {
	Validate(yamlBytes []byte) (domain.Manifest, []string)
}

const (
	defaultPageSize        = 50
	maxPageSize            = 100
	maxIdempotencyKeyRunes = 128
)

type Service struct {
	repository ports.Repository
	validator  ManifestValidator
	projects   ProjectDirectory
	ids        ids.Generator
	now        func() time.Time
}

func New(repository ports.Repository, validator ManifestValidator, projects ProjectDirectory, generator ids.Generator) (*Service, error) {
	if repository == nil || validator == nil || generator == nil {
		return nil, errors.New("app registry requires repository, validator, and id generator")
	}
	return &Service{
		repository: repository, validator: validator, projects: projects,
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
func (s *Service) Register(ctx context.Context, ownerUserID, idempotencyKey string, yamlBytes []byte) (domain.AppVersion, error) {
	if ownerUserID == "" || idempotencyKey == "" || len([]rune(idempotencyKey)) > maxIdempotencyKeyRunes || len(yamlBytes) == 0 || len(yamlBytes) > manifestLimit() {
		return domain.AppVersion{}, domain.ErrInvalid
	}
	manifest, violations := s.validator.Validate(yamlBytes)
	if len(violations) > 0 {
		return domain.AppVersion{}, domain.ErrInvalid
	}
	if _, ok := domain.ParseVersion(manifest.Version); !ok {
		return domain.AppVersion{}, domain.ErrInvalid
	}
	record := domain.AppVersion{
		ID: s.ids.New(), OwnerUserID: ownerUserID, AppID: manifest.ID, Version: manifest.Version,
		Scope: manifest.Scope, Name: manifest.Name, Permissions: manifest.Permissions,
		ManifestDigest: manifest.Digest, CanonicalManifest: manifest.CanonicalJSON,
		IdempotencyKey: idempotencyKey, RequestDigest: manifest.Digest, CreatedAt: s.now(),
	}
	return s.repository.Register(ctx, record)
}

// Get returns the current version for an empty version, or the exact
// immutable version when one is requested.
func (s *Service) Get(ctx context.Context, ownerUserID, appID, version string) (domain.AppVersion, error) {
	if ownerUserID == "" || appID == "" {
		return domain.AppVersion{}, domain.ErrInvalid
	}
	if version != "" {
		if _, ok := domain.ParseVersion(version); !ok {
			return domain.AppVersion{}, domain.ErrInvalid
		}
		return s.repository.GetVersion(ctx, ownerUserID, appID, version)
	}
	versions, err := s.repository.GetAppVersions(ctx, ownerUserID, appID)
	if err != nil {
		return domain.AppVersion{}, err
	}
	current, ok := domain.CurrentVersion(versions)
	if !ok {
		return domain.AppVersion{}, domain.ErrNotFound
	}
	return current, nil
}

// List returns the current version of every registered app, ordered by app
// ID, one page at a time. A non-empty projectID first proves the project
// belongs to the owner and is not archived; the result is the owner's
// registry catalog in that project context, never an installation state.
func (s *Service) List(ctx context.Context, ownerUserID, projectID, cursor string, pageSize int) ([]domain.AppVersion, error) {
	if ownerUserID == "" {
		return nil, domain.ErrInvalid
	}
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	if projectID != "" {
		if s.projects == nil {
			return nil, errors.New("project directory is not configured")
		}
		if _, err := s.projects.Get(ctx, ownerUserID, projectID); err != nil {
			if errors.Is(err, ErrProjectDenied) {
				return nil, domain.ErrNotFound
			}
			return nil, fmt.Errorf("resolve project context: %w", err)
		}
	}
	appIDs, err := s.repository.ListAppIDs(ctx, ownerUserID, cursor, pageSize)
	if err != nil {
		return nil, err
	}
	if len(appIDs) == 0 {
		return []domain.AppVersion{}, nil
	}
	versions, err := s.repository.GetVersionsForApps(ctx, ownerUserID, appIDs)
	if err != nil {
		return nil, err
	}
	grouped := make(map[string][]domain.AppVersion, len(appIDs))
	for _, version := range versions {
		grouped[version.AppID] = append(grouped[version.AppID], version)
	}
	result := make([]domain.AppVersion, 0, len(appIDs))
	for _, appID := range appIDs {
		if current, ok := domain.CurrentVersion(grouped[appID]); ok {
			result = append(result, current)
		}
	}
	return result, nil
}

func manifestLimit() int {
	return domain.MaxManifestBytes
}
