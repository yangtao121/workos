// Package application serves the runtime-host workspace host service
// (ADR-0030): operator-registered directories, prepared execution
// environments, and the shared directory resolution consumed by the PTY and
// native runners.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/platform/config"
)

var (
	ErrNoWorkspace  = errors.New("no workspace registered for this owner and project")
	ErrInvalidScope = errors.New("invalid workspace scope")
)

const (
	maximumSources      = 32
	environmentValidity = 30 * time.Minute
	kindLocalGit        = "local_git"
	kindLocalDirectory  = "local_directory"
)

// Source is one operator-registered directory scoped to an owner and
// project. The path itself never leaves this process.
type Source struct {
	ID          string
	OwnerUserID string
	ProjectID   string
	Path        string
	DisplayName string
	ReadOnly    bool
	Kind        string
	Registered  time.Time
}

// Environment is a prepared execution environment handle for one consumer.
type Environment struct {
	ID         string
	SourceID   string
	ReadOnly   bool
	PreparedAt time.Time
	ExpiresAt  time.Time
}

// Service owns the operator-configured workspace sources of this runtime
// host. All lookups are scope-bound: an owner never resolves another
// owner's directory.
type Service struct {
	sources []Source
	byScope map[string]Source
}

func sourceID(ownerUserID, projectID string) string {
	digest := sha256.Sum256([]byte(ownerUserID + "\x00" + projectID))
	return "ws_" + hex.EncodeToString(digest[:8])
}

func scopeKey(ownerUserID, projectID string) string {
	return ownerUserID + "\x00" + projectID
}

// New builds the service from validated deployment configuration. It never
// reads the directories themselves; mount usability is owned by the
// openat2-backed file store.
func New(now time.Time, mounts []config.WorkspaceMount) (*Service, error) {
	if len(mounts) > maximumSources {
		return nil, ErrInvalidScope
	}
	service := &Service{byScope: make(map[string]Source, len(mounts))}
	for i, mount := range mounts {
		if mount.OwnerUserID == "" || mount.ProjectID == "" || !filepath.IsAbs(mount.RootPath) || filepath.Clean(mount.RootPath) != mount.RootPath || mount.RootPath == "/" {
			return nil, ErrInvalidScope
		}
		for _, other := range mounts[:i] {
			if mount.RootPath == other.RootPath ||
				strings.HasPrefix(mount.RootPath, other.RootPath+string(filepath.Separator)) ||
				strings.HasPrefix(other.RootPath, mount.RootPath+string(filepath.Separator)) {
				return nil, ErrInvalidScope
			}
		}
		kind := kindLocalDirectory
		if _, err := statGitDirectory(mount.RootPath); err == nil {
			kind = kindLocalGit
		}
		source := Source{
			ID:          sourceID(mount.OwnerUserID, mount.ProjectID),
			OwnerUserID: mount.OwnerUserID,
			ProjectID:   mount.ProjectID,
			Path:        mount.RootPath,
			DisplayName: filepath.Base(mount.RootPath),
			ReadOnly:    mount.ReadOnly,
			Kind:        kind,
			Registered:  now,
		}
		if _, duplicate := service.byScope[scopeKey(mount.OwnerUserID, mount.ProjectID)]; duplicate {
			return nil, ErrInvalidScope
		}
		service.byScope[scopeKey(mount.OwnerUserID, mount.ProjectID)] = source
		service.sources = append(service.sources, source)
	}
	sort.Slice(service.sources, func(i, j int) bool { return service.sources[i].ID < service.sources[j].ID })
	return service, nil
}

// Sources lists the sources registered for one owner.
func (s *Service) Sources(ownerUserID string) []Source {
	owned := make([]Source, 0, len(s.sources))
	for _, source := range s.sources {
		if source.OwnerUserID == ownerUserID {
			owned = append(owned, source)
		}
	}
	return owned
}

// SourceByID finds one owner's source by id.
func (s *Service) SourceByID(ownerUserID, id string) (Source, bool) {
	for _, source := range s.sources {
		if source.OwnerUserID == ownerUserID && source.ID == id {
			return source, true
		}
	}
	return Source{}, false
}

// Resolve returns the concrete directory for an owner and project. The bool
// reports whether a source exists; absence is a normal state (the project
// has no development workspace on this host).
func (s *Service) Resolve(ownerUserID, projectID string) (Source, bool) {
	source, ok := s.byScope[scopeKey(ownerUserID, projectID)]
	return source, ok
}

// Prepare issues the environment handle for one consumer. It performs no
// filesystem mutation; the openat2-backed store keeps owning usability.
func (s *Service) Prepare(_ context.Context, ownerUserID, projectID, consumer string, now time.Time) (Environment, error) {
	switch consumer {
	case "harness", "terminal", "native", "files":
	default:
		return Environment{}, ErrInvalidScope
	}
	source, ok := s.Resolve(ownerUserID, projectID)
	if !ok {
		return Environment{}, ErrNoWorkspace
	}
	digest := sha256.Sum256([]byte(source.ID + "\x00" + consumer + "\x00" + now.UTC().Format(time.RFC3339Nano)))
	return Environment{
		ID:         "env_" + hex.EncodeToString(digest[:12]),
		SourceID:   source.ID,
		ReadOnly:   source.ReadOnly,
		PreparedAt: now.UTC(),
		ExpiresAt:  now.Add(environmentValidity).UTC(),
	}, nil
}

// statGitDirectory reports whether the directory carries a .git entry. It is
// advisory only: the kind never gates access.
func statGitDirectory(root string) (struct{}, error) {
	var probe struct{}
	entries, err := filepath.Glob(strings.Join([]string{root, ".git"}, string(filepath.Separator)))
	if err != nil || len(entries) == 0 {
		return probe, errNoGit
	}
	return probe, nil
}

var errNoGit = errors.New("no .git entry")

// WorkingDirectory resolves the operator-registered directory for one
// owner's project, satisfying the PTY and native runner resolver ports.
func (s *Service) WorkingDirectory(ownerUserID, projectID string) (string, bool) {
	source, ok := s.Resolve(ownerUserID, projectID)
	if !ok {
		return "", false
	}
	return source.Path, true
}
