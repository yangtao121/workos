package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ReleaseStatus is the owner-facing sanitized projection of one installation's
// newest deployment ledger row (ADR-0033). It never carries engine internals.
type ReleaseStatus struct {
	InstallationID          string
	AppID                   string
	State                   string
	CandidateVersion        string
	CandidateArtifactDigest string
	BaseVersion             string
	BaseArtifactDigest      string
	FailureCategory         string
	UpdatedAt               time.Time
	Revision                int64
}

type ReleaseLookups interface {
	LatestDeployment(ctx context.Context, owner, projectID, installationID string) (DeploymentRecord, error)
}

type ReleaseService struct {
	lookups ReleaseLookups
}

func NewReleaseService(lookups ReleaseLookups) (*ReleaseService, error) {
	if lookups == nil {
		return nil, errors.New("release service requires a ledger lookup")
	}
	return &ReleaseService{lookups: lookups}, nil
}

func (s *ReleaseService) GetStatus(ctx context.Context, owner, projectID, installationID string) (ReleaseStatus, error) {
	for _, id := range []string{owner, projectID, installationID} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 || parsed.String() != id {
			return ReleaseStatus{}, ErrDeploymentCandidateRequired
		}
	}
	row, err := s.lookups.LatestDeployment(ctx, owner, projectID, installationID)
	if err != nil {
		return ReleaseStatus{}, err
	}
	return ReleaseStatus{
		InstallationID:          row.InstallationID,
		State:                   projectReleaseState(row.State),
		CandidateVersion:        row.TargetVersion,
		CandidateArtifactDigest: row.ArtifactDigest,
		BaseVersion:             row.BaseVersion,
		BaseArtifactDigest:      row.BaseArtifactDigest,
		UpdatedAt:               row.UpdatedAt,
		Revision:                row.UpdatedAt.UnixMicro(),
	}, nil
}

func projectReleaseState(ledger string) string {
	switch ledger {
	case DeploymentCandidateState:
		return "staged"
	case DeploymentStarting:
		return "starting"
	case DeploymentCanary:
		return "canary"
	case DeploymentPromoted:
		return "published"
	case DeploymentRollback, DeploymentRollbackPending:
		return "rollback_pending"
	case DeploymentRolledBack:
		return "rolled_back"
	case DeploymentSuperseded:
		return "superseded"
	case DeploymentFailed:
		return "failed"
	default:
		return "idle"
	}
}
