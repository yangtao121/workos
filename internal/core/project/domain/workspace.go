package domain

import (
	"errors"
	"time"
)

// Project workspace binding facts (ADR-0030): Core owns ownership and
// authorization; the runtime owns the directory. Clients reference
// operator-registered sources by id and never submit host paths.
var (
	ErrWorkspaceNotFound      = errors.New("workspace binding not found")
	ErrWorkspaceSourceUnknown = errors.New("workspace source is not registered by the operator")
	ErrWorkspaceRevision      = errors.New("workspace binding revision mismatch")
	ErrWorkspaceArchived      = errors.New("workspace binding is archived")
	ErrWorkspaceConflict      = errors.New("workspace binding idempotency key was used for a different request")
	ErrWorkspaceActiveExists  = errors.New("project already has an active workspace binding")
)

type WorkspaceBindingState string

const (
	WorkspaceBindingActive   WorkspaceBindingState = "active"
	WorkspaceBindingArchived WorkspaceBindingState = "archived"
)

type WorkspaceBinding struct {
	ID                string
	OwnerUserID       string
	ProjectID         string
	WorkspaceSourceID string
	IdempotencyKey    string
	DisplayName       string
	ReadOnly          bool
	State             WorkspaceBindingState
	Revision          int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ArchivedAt        *time.Time
}

// ValidWorkspaceSourceID enforces the opaque source reference grammar; it
// never carries a host path.
func ValidWorkspaceSourceID(id string) bool {
	if len(id) < 4 || len(id) > 128 {
		return false
	}
	for _, char := range id {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '_':
		default:
			return false
		}
	}
	return true
}
