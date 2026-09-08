package orchestration

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	catalogapp "github.com/yangtao121/workos/internal/core/harnesscatalog/application"
	catalogdomain "github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
)

type admissionCatalog struct {
	health catalogdomain.Health
	reads  int
}

func (s *admissionCatalog) ListProviders(context.Context) ([]catalogdomain.Provider, error) {
	s.reads++
	return []catalogdomain.Provider{{ID: "fake", Health: s.health, Capabilities: catalogdomain.Capabilities{StructuredArtifacts: true, SupportedArtifactTypes: []string{"document.markdown.v1"}, SupportedContextRefTypes: []string{"artifact.review.v1"}}}}, nil
}
func TestAdmissionRequiresHealthyProviderAndPreservesReplay(t *testing.T) {
	for _, health := range []catalogdomain.Health{catalogdomain.HealthHealthy, catalogdomain.HealthStarting, catalogdomain.HealthDegraded, catalogdomain.HealthUnavailable, catalogdomain.HealthUnknown} {
		t.Run(string(health), func(t *testing.T) {
			source := &admissionCatalog{health: health}
			catalog, err := catalogapp.New(source, "fake")
			if err != nil {
				t.Fatal(err)
			}
			providers, err := NewProviderCapabilities(catalog)
			if err != nil {
				t.Fatal(err)
			}
			agents := &fakeAgents{}
			router, err := NewTaskRouter(agents, &fakeProjects{}, &fakePolicies{}, providers, fakeCredentials{}, stubContextVerifier{}, "fake")
			if err != nil {
				t.Fatal(err)
			}
			input := agentapp.SubmitInput{OwnerUserID: "owner", ProjectID: "project", IdempotencyKey: "key", Payload: []byte(`{"goal":"repair"}`)}
			task, err := router.Submit(context.Background(), input)
			if health == catalogdomain.HealthHealthy {
				if err != nil || len(agents.submitted) != 1 {
					t.Fatalf("healthy admission: %v", err)
				}
				agents.existing = &task
				source.health = catalogdomain.HealthUnavailable
				replay, err := router.Submit(context.Background(), input)
				if err != nil || replay.ID != task.ID || source.reads != 1 || len(agents.submitted) != 1 {
					t.Fatalf("replay depended on current health: %v reads=%d", err, source.reads)
				}
			} else if health == catalogdomain.HealthDegraded {
				// Degraded still admits: transient upstream failures only
				// recover through a subsequent run.
				if err != nil || len(agents.submitted) != 1 {
					t.Fatalf("degraded admission: %v submissions=%d", err, len(agents.submitted))
				}
			} else if !errors.Is(err, agentdomain.ErrProviderUnavailable) || len(agents.submitted) != 0 {
				t.Fatalf("unhealthy provider admitted: %v submissions=%d", err, len(agents.submitted))
			}
		})
	}
}
func TestAdmissionUsesOneCapabilitySnapshot(t *testing.T) {
	source := &admissionCatalog{health: catalogdomain.HealthHealthy}
	catalog, err := catalogapp.New(source, "fake")
	if err != nil {
		t.Fatal(err)
	}
	providers, err := NewProviderCapabilities(catalog)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewTaskRouter(&fakeAgents{}, &fakeProjects{}, &fakePolicies{}, providers, fakeCredentials{}, stubContextVerifier{}, "fake")
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Submit(context.Background(), agentapp.SubmitInput{OwnerUserID: "owner", ProjectID: "project", IdempotencyKey: "key", Payload: []byte(`{"goal":"review"}`), OutputArtifactTypes: []string{"document.markdown.v1"}, ContextRefs: []agentports.ContextRef{{Type: "artifact.review.v1", ID: "artifact", Revision: "digest"}}})
	if err != nil || source.reads != 1 {
		t.Fatalf("inconsistent capability snapshots: %v reads=%d", err, source.reads)
	}
}
