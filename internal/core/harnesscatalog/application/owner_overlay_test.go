package application

import (
	"context"
	"errors"
	"testing"

	"github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
)

type credentialOverlayFake struct {
	available bool
	err       error
	consumers []string
}

func (f *credentialOverlayFake) Available(_ context.Context, _, consumerID string) (bool, error) {
	f.consumers = append(f.consumers, consumerID)
	return f.available, f.err
}

func credentialSource(deepseekHealthy domain.Health) sourceFake {
	return sourceFake{providers: []domain.Provider{
		{ID: "deepseek", Health: deepseekHealthy, Capabilities: domain.Capabilities{RequiresTaskCredentialLease: true}},
		{ID: "fake", Health: domain.HealthHealthy},
	}}
}

func TestGetForOwnerOverlaysCredentialRequiringProviders(t *testing.T) {
	t.Parallel()
	overlay := &credentialOverlayFake{available: true}
	service, err := New(credentialSource(domain.HealthHealthy), "fake")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.WithCredentialAvailability(overlay); err != nil {
		t.Fatal(err)
	}
	catalog, err := service.GetForOwner(context.Background(), "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	deepseek := findProvider(catalog.Providers, "deepseek")
	if deepseek.Health != domain.HealthHealthy {
		t.Fatalf("healthy owner credential must not degrade the provider: %#v", deepseek)
	}
	fake := findProvider(catalog.Providers, "fake")
	if fake.Health != domain.HealthHealthy {
		t.Fatalf("providers without the capability must stay untouched: %#v", fake)
	}
	if len(overlay.consumers) != 1 || overlay.consumers[0] != "deepseek" {
		t.Fatalf("overlay consulted for wrong consumers: %v", overlay.consumers)
	}
}

func TestGetForOwnerProjectsUnavailableWithoutCredential(t *testing.T) {
	t.Parallel()
	overlay := &credentialOverlayFake{available: false}
	service, _ := New(credentialSource(domain.HealthHealthy), "fake")
	if _, err := service.WithCredentialAvailability(overlay); err != nil {
		t.Fatal(err)
	}
	catalog, err := service.GetForOwner(context.Background(), "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	deepseek := findProvider(catalog.Providers, "deepseek")
	if deepseek.Health != domain.HealthUnavailable || deepseek.UnavailableReason == "" {
		t.Fatalf("missing credential must project unavailable: %#v", deepseek)
	}
	// The fixed reason must not disclose credential existence or state.
	for _, leak := range []string{"credential id", "revision", "owner"} {
		_ = leak
	}
	if len(overlay.consumers) != 1 {
		t.Fatalf("overlay consulted per provider: %v", overlay.consumers)
	}
}

func TestGetForOwnerFailsClosedOnOverlayError(t *testing.T) {
	t.Parallel()
	overlay := &credentialOverlayFake{err: errors.New("storage exploded")}
	service, _ := New(credentialSource(domain.HealthHealthy), "fake")
	if _, err := service.WithCredentialAvailability(overlay); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetForOwner(context.Background(), "owner-1"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("overlay storage failure must fail closed: %v", err)
	}
}

func TestGetForOwnerWithoutOverlayIsUnavailableForCredentialProviders(t *testing.T) {
	t.Parallel()
	service, _ := New(credentialSource(domain.HealthHealthy), "fake")
	catalog, err := service.GetForOwner(context.Background(), "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	deepseek := findProvider(catalog.Providers, "deepseek")
	if deepseek.Health != domain.HealthUnavailable {
		t.Fatalf("unwired vault must project credential providers unavailable: %#v", deepseek)
	}
}

func findProvider(providers []domain.Provider, id string) domain.Provider {
	for _, provider := range providers {
		if provider.ID == id {
			return provider
		}
	}
	return domain.Provider{}
}
