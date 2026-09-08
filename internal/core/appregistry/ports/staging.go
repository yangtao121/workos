package ports

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// StagedVersion is the projection of one staged repair candidate version.
type StagedVersion struct {
	AppID          string
	Version        string
	ManifestDigest string
	State          string
}

// CandidateVersionMapping is the durable task → staged-version fact.
type CandidateVersionMapping struct {
	TaskID         string
	OwnerUserID    string
	ProjectID      string
	InstallationID string
	IncidentID     string
	BuildJobID     string
	SourceDigest   string
	AppVersionID   string
	PublishedAt    *time.Time
}

// StagingStore owns the staged-version lifecycle inside the coordinator's
// transaction (ADR-0026). All methods participate in the caller's tx.
type StagingStore interface {
	// FindCandidateVersion replays an existing task mapping idempotently.
	FindCandidateVersion(ctx context.Context, tx dbtx.Tx, taskID string) (CandidateVersionMapping, error)
	// GetVersionAnyState reads one version summary regardless of state.
	GetVersionAnyState(ctx context.Context, tx dbtx.Tx, versionID string) (StagedVersion, error)
	// InsertStagedVersion persists the derived immutable staged version.
	InsertStagedVersion(ctx context.Context, tx dbtx.Tx, version domain.AppVersion, createdAt time.Time) (string, error)
	// InsertCandidateVersion binds the task to its staged version.
	InsertCandidateVersion(ctx context.Context, tx dbtx.Tx, mapping CandidateVersionMapping, createdAt time.Time) error
	// PublishStagedVersion flips staged→published; already published is a
	// deterministic no-op returning published=false.
	PublishStagedVersion(ctx context.Context, tx dbtx.Tx, taskID, versionID string, publishedAt time.Time) (bool, error)
}
