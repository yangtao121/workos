package domain

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid project")
	ErrNotFound = errors.New("project not found")
	ErrConflict = errors.New("project revision conflict")
)

type WorkspaceRef struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	URI          string `json:"uri"`
	LogicalMount string `json:"logicalMount"`
	ReadOnly     bool   `json:"readOnly"`
}

type HarnessBinding struct {
	ProviderID       string `json:"providerId"`
	InstancePolicy   string `json:"instancePolicy"`
	ProfileID        string `json:"profileId,omitempty"`
	CredentialRef    string `json:"credentialRef,omitempty"`
	ResourcePolicyID string `json:"resourcePolicyId"`
}

type Project struct {
	ID                    string
	OwnerUserID           string
	Name                  string
	Icon                  string
	WorkspaceRefs         []WorkspaceRef
	HarnessBinding        *HarnessBinding
	InstalledAppIDs       []string
	DefaultAgentRole      string
	KnowledgeCollectionID string
	ArtifactCollectionID  string
	Revision              int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	ArchivedAt            *time.Time
}

func NormalizeName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > 120 {
		return "", ErrInvalid
	}
	return value, nil
}

func ValidateBinding(binding *HarnessBinding) error {
	if binding == nil {
		return nil
	}
	if binding.ProviderID == "" || binding.ResourcePolicyID == "" {
		return ErrInvalid
	}
	switch binding.InstancePolicy {
	case "persistent", "lazy", "ephemeral":
		return nil
	default:
		return ErrInvalid
	}
}
