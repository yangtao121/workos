package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
)

type sourceFake struct {
	providers []domain.Provider
	err       error
}

func (f sourceFake) ListProviders(context.Context) ([]domain.Provider, error) {
	return f.providers, f.err
}

func TestCatalogNormalizesFieldsCapabilitiesAndOrder(t *testing.T) {
	t.Parallel()
	all := domain.Capabilities{
		Streaming: true, PersistentSessions: true, Resume: true, SteerDuringRun: true,
		Approvals: true, ToolRegistration: true, MCP: true, Subagents: true,
		WorkspaceMount: true, StructuredArtifacts: true, UsageReporting: true,
		SupportedArtifactTypes: []string{"document.markdown.v1", "code.unified-diff.v1"},
	}
	service, err := New(sourceFake{providers: []domain.Provider{
		{ID: "zeta", DisplayName: "", AdapterVersion: "", Health: domain.Health("")},
		{ID: "alpha", DisplayName: "  Alpha\nHarness  ", AdapterVersion: " 1.2.3 ", Health: domain.HealthDegraded, Capabilities: all},
		{ID: "healthy", DisplayName: "Healthy", AdapterVersion: "1", Health: domain.HealthHealthy, UnavailableReason: "Authorization: Bearer secret"},
	}}, "fake")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if catalog.DefaultProviderID != "fake" || len(catalog.Providers) != 3 || catalog.Providers[0].ID != "alpha" || catalog.Providers[2].ID != "zeta" {
		t.Fatalf("unexpected catalog order/default: %#v", catalog)
	}
	alpha := catalog.Providers[0]
	if alpha.DisplayName != "Alpha Harness" || alpha.AdapterVersion != "1.2.3" || alpha.UnavailableReason != "Provider is temporarily degraded" || !capabilitiesEqual(alpha.Capabilities, all) {
		t.Fatalf("provider fields were not mapped: %#v", alpha)
	}
	if got := catalog.Providers[1].UnavailableReason; got != "" {
		t.Fatalf("healthy provider exposed a private reason: %q", got)
	}
	zeta := catalog.Providers[2]
	if zeta.DisplayName != "zeta" || zeta.AdapterVersion != "unknown" || zeta.UnavailableReason != "Provider health is unknown" {
		t.Fatalf("fallback fields are not deterministic: %#v", zeta)
	}
}

func TestCatalogRejectsInvalidAndDuplicateProviderIDs(t *testing.T) {
	t.Parallel()
	for _, providers := range [][]domain.Provider{
		{{ID: ""}},
		{{ID: " padded "}},
		{{ID: "bad\nvalue"}},
		{{ID: strings.Repeat("x", maximumProviderIDBytes+1)}},
		{{ID: "same"}, {ID: "same"}},
	} {
		service, err := New(sourceFake{providers: providers}, "fake")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Get(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("expected invalid catalog to be unavailable, got %v for %#v", err, providers)
		}
	}
}

func TestCatalogBoundsNonIdentityText(t *testing.T) {
	t.Parallel()
	service, err := New(sourceFake{providers: []domain.Provider{{
		ID: "provider", DisplayName: strings.Repeat("名", maximumDisplayNameRunes+20),
		AdapterVersion: strings.Repeat("v", maximumAdapterVersionRunes+20), Health: domain.HealthStarting,
	}}}, "fake")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	provider := catalog.Providers[0]
	if len([]rune(provider.DisplayName)) != maximumDisplayNameRunes || len([]rune(provider.AdapterVersion)) != maximumAdapterVersionRunes {
		t.Fatalf("provider text was not bounded: name=%d version=%d", len([]rune(provider.DisplayName)), len([]rune(provider.AdapterVersion)))
	}
}

func TestCatalogPreservesCancellationAndDeadline(t *testing.T) {
	t.Parallel()
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		service, err := New(sourceFake{err: want}, "fake")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Get(context.Background()); !errors.Is(err, want) {
			t.Fatalf("expected %v, got %v", want, err)
		}
	}
}

// capabilitiesEqual compares capability facts; the supported artifact type
// list is a slice, so struct equality cannot be used directly.
func capabilitiesEqual(a, b domain.Capabilities) bool {
	return reflect.DeepEqual(a, b)
}
