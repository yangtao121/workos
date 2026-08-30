package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/reliability/domain"
	"github.com/yangtao121/workos/internal/reliability/ports"
)

// IncidentService exposes the owner-facing incident use cases: bounded
// owner-scoped listing and the one-way acknowledgement. Acknowledge records
// that the owner reviewed the incident; it never claims the fault is
// repaired — mitigation and resolution are the supervisor's separate facts
// (ADR-0006 §6).
type IncidentService struct {
	repository ports.IncidentRepository
	now        func() time.Time
}

func NewIncidentService(repository ports.IncidentRepository) (*IncidentService, error) {
	return NewIncidentServiceWithClock(repository, func() time.Time { return time.Now().UTC() })
}

func NewIncidentServiceWithClock(repository ports.IncidentRepository, now func() time.Time) (*IncidentService, error) {
	if repository == nil || now == nil {
		return nil, errors.New("incident service requires a repository and clock")
	}
	return &IncidentService{repository: repository, now: now}, nil
}

// Bounds of the public list: page sizes are clamped server-side and the
// fetch probes one extra row for exact next-page tokens.
const (
	minPageSize = 1
	maxPageSize = 50
)

// Get returns one owner-scoped incident. Foreign or unknown incidents are
// the same sanitized NotFound — existence never leaks across owners.
func (s *IncidentService) Get(ctx context.Context, ownerUserID, incidentID string) (domain.Incident, error) {
	if ownerUserID == "" || !domain.ValidUUIDv7(incidentID) {
		return domain.Incident{}, domain.ErrInvalid
	}
	incident, err := s.repository.GetIncident(ctx, incidentID)
	if err != nil {
		return domain.Incident{}, err
	}
	if incident.OwnerUserID != ownerUserID {
		return domain.Incident{}, domain.ErrNotFound
	}
	return incident, nil
}

// List returns one owner-scoped page ordered oldest-first within the page.
// The next-page token is the last incident's ID and only ever points at a
// real boundary: the repository probes limit+1 rows so a full final page
// never phantom-pages.
func (s *IncidentService) List(ctx context.Context, ownerUserID, projectID string, pageSize int, pageToken string) ([]domain.Incident, string, error) {
	if ownerUserID == "" {
		return nil, "", domain.ErrInvalid
	}
	if projectID != "" && !domain.ValidUUIDv7(projectID) {
		return nil, "", domain.ErrInvalid
	}
	limit := pageSize
	if limit == 0 {
		limit = 20
	}
	if limit < minPageSize || limit > maxPageSize {
		return nil, "", domain.ErrInvalid
	}
	if pageToken != "" && !domain.ValidUUIDv7(pageToken) {
		return nil, "", domain.ErrInvalid
	}
	incidents, err := s.repository.ListIncidents(ctx, ports.IncidentFilter{
		OwnerUserID: ownerUserID, ProjectID: projectID, PageToken: pageToken,
	}, limit+1)
	if err != nil {
		if errors.Is(err, domain.ErrUnavailable) {
			return nil, "", domain.ErrUnavailable
		}
		return nil, "", fmt.Errorf("list incidents: %w", err)
	}
	next := ""
	if len(incidents) > limit {
		incidents = incidents[:limit]
		next = incidents[len(incidents)-1].ID
	}
	return incidents, next, nil
}

// Acknowledge stamps the owner acknowledgement with a durable idempotency
// key: same key replays the same result; a foreign incident is NotFound; a
// reused key on a different incident is a stable conflict.
func (s *IncidentService) Acknowledge(ctx context.Context, ownerUserID, incidentID, idempotencyKey string) (domain.Incident, error) {
	if ownerUserID == "" || !domain.ValidUUIDv7(incidentID) || !domain.ValidIdempotencyKey(idempotencyKey) {
		return domain.Incident{}, domain.ErrInvalid
	}
	incident, err := s.repository.GetIncident(ctx, incidentID)
	if err != nil {
		return domain.Incident{}, err
	}
	if incident.OwnerUserID != ownerUserID {
		return domain.Incident{}, domain.ErrNotFound
	}
	if err := s.repository.Acknowledge(ctx, incidentID, ownerUserID, idempotencyKey, s.now()); err != nil {
		return domain.Incident{}, err
	}
	return s.repository.GetIncident(ctx, incidentID)
}
