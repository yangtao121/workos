package application

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
	"github.com/yangtao121/workos/internal/core/harnesscatalog/ports"
)

const (
	maximumProviderIDBytes        = 128
	maximumDisplayNameRunes       = 120
	maximumAdapterVersionRunes    = 64
	maximumUnavailableReasonRunes = 240
)

type Service struct {
	source            ports.Source
	defaultProviderID string
	// credentials is the optional owner-aware vault overlay. When it is nil
	// the vault is not wired and every credential-requiring provider is
	// projected unavailable to every owner — never silently selectable.
	credentials ports.CredentialAvailability
}

func New(source ports.Source, defaultProviderID string) (*Service, error) {
	if source == nil || !validProviderID(defaultProviderID) {
		return nil, errors.New("catalog requires a source and valid default provider")
	}
	return &Service{source: source, defaultProviderID: defaultProviderID}, nil
}

// WithCredentialAvailability wires the owner-aware credential overlay.
func (s *Service) WithCredentialAvailability(credentials ports.CredentialAvailability) (*Service, error) {
	if credentials == nil {
		return nil, errors.New("catalog credential overlay requires an availability source")
	}
	s.credentials = credentials
	return s, nil
}

// GetForOwner is the public catalog projection for one owner: providers that
// require a task credential lease are unavailable unless the vault holds an
// active credential for this owner and consumer. The reason is a fixed safe
// sentence: it never discloses whether a foreign credential, credential ID,
// revision, or key state exists. An overlay storage failure fails closed for
// the whole read.
func (s *Service) GetForOwner(ctx context.Context, ownerUserID string) (domain.Catalog, error) {
	catalog, err := s.Get(ctx)
	if err != nil {
		return domain.Catalog{}, err
	}
	if ownerUserID == "" {
		return domain.Catalog{}, domain.ErrUnavailable
	}
	for index := range catalog.Providers {
		provider := &catalog.Providers[index]
		if !provider.Capabilities.RequiresTaskCredentialLease {
			continue
		}
		if s.credentials == nil {
			provider.Health = domain.HealthUnavailable
			provider.UnavailableReason = publicReason(domain.HealthUnavailable)
			continue
		}
		available, err := s.credentials.Available(ctx, ownerUserID, provider.ID)
		if err != nil {
			return domain.Catalog{}, domain.ErrUnavailable
		}
		if !available {
			provider.Health = domain.HealthUnavailable
			provider.UnavailableReason = publicReason(domain.HealthUnavailable)
		}
	}
	return catalog, nil
}

func (s *Service) Get(ctx context.Context) (domain.Catalog, error) {
	providers, err := s.source.ListProviders(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return domain.Catalog{}, err
		}
		return domain.Catalog{}, domain.ErrUnavailable
	}

	seen := make(map[string]struct{}, len(providers))
	result := make([]domain.Provider, 0, len(providers))
	for _, provider := range providers {
		if !validProviderID(provider.ID) {
			return domain.Catalog{}, domain.ErrUnavailable
		}
		if _, exists := seen[provider.ID]; exists {
			return domain.Catalog{}, domain.ErrUnavailable
		}
		seen[provider.ID] = struct{}{}
		if !validArtifactCapability(provider.Capabilities.StructuredArtifacts, provider.Capabilities.SupportedArtifactTypes) {
			// Bool/list drift is adapter capability corruption: Core must
			// fail closed on the whole catalog read, never assume "all
			// types" or silently drop the list (ADR-0008).
			return domain.Catalog{}, domain.ErrUnavailable
		}
		if !validContextRefCapability(provider.Capabilities.SupportedContextRefTypes) {
			// Unknown or duplicated context ref types are capability
			// corruption, never "all types" (ADR-0010).
			return domain.Catalog{}, domain.ErrUnavailable
		}
		provider.DisplayName = boundedText(provider.DisplayName, maximumDisplayNameRunes)
		if provider.DisplayName == "" {
			provider.DisplayName = provider.ID
		}
		provider.AdapterVersion = boundedText(provider.AdapterVersion, maximumAdapterVersionRunes)
		if provider.AdapterVersion == "" {
			provider.AdapterVersion = "unknown"
		}
		provider.UnavailableReason = publicReason(provider.Health)
		result = append(result, provider)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return domain.Catalog{Providers: result, DefaultProviderID: s.defaultProviderID}, nil
}

func validProviderID(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maximumProviderIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// maxSupportedArtifactTypes bounds the declared supported-artifact list.
const maxSupportedArtifactTypes = 16

// validArtifactCapability enforces the exact structured-artifact capability
// contract: enabled means a non-empty, bounded list of bounded type strings
// without duplicates; disabled means an empty list.
func validArtifactCapability(enabled bool, types []string) bool {
	if !enabled {
		return len(types) == 0
	}
	if len(types) == 0 || len(types) > maxSupportedArtifactTypes {
		return false
	}
	seen := make(map[string]struct{}, len(types))
	for _, artifactType := range types {
		if artifactType == "" || len(artifactType) > 128 || !utf8.ValidString(artifactType) {
			return false
		}
		for _, character := range artifactType {
			if unicode.IsControl(character) {
				return false
			}
		}
		if _, duplicate := seen[artifactType]; duplicate {
			return false
		}
		seen[artifactType] = struct{}{}
	}
	return true
}

// validContextRefCapability enforces the exact context ref type contract:
// empty (unsupported) or a bounded list of known canonical types without
// duplicates. Values outside the canonical vocabulary fail closed.
func validContextRefCapability(types []string) bool {
	if len(types) == 0 {
		return true
	}
	if len(types) > maxSupportedArtifactTypes {
		return false
	}
	seen := make(map[string]struct{}, len(types))
	for _, contextType := range types {
		switch contextType {
		case "artifact.review.v1":
		default:
			return false
		}
		if _, duplicate := seen[contextType]; duplicate {
			return false
		}
		seen[contextType] = struct{}{}
	}
	return true
}

func boundedText(value string, maximum int) string {
	value = strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
	clean := make([]rune, 0, len(value))
	for _, character := range value {
		if !unicode.IsControl(character) {
			clean = append(clean, character)
		}
		if len(clean) == maximum {
			break
		}
	}
	return strings.TrimSpace(string(clean))
}

func publicReason(health domain.Health) string {
	var reason string
	switch health {
	case domain.HealthHealthy:
		return ""
	case domain.HealthStarting:
		reason = "Provider is starting"
	case domain.HealthDegraded:
		reason = "Provider is temporarily degraded"
	case domain.HealthUnavailable:
		reason = "Provider is disabled or misconfigured"
	default:
		reason = "Provider health is unknown"
	}
	return boundedText(reason, maximumUnavailableReasonRunes)
}
