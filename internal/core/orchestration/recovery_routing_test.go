package orchestration

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

// recoveryProviders serves a healthy primary ("fake") and a configurable
// recovery tier so the fallback matrix exercises real verdicts.
type recoveryProviders struct {
	candidates     bool
	primaryErr     error
	recoveryErr    error
	recoveryHealth bool
	lookups         []string
}

func (f *recoveryProviders) Capabilities(_ context.Context, providerID string) (agentports.ProviderCapabilities, error) {
	f.lookups = append(f.lookups, providerID)
	if providerID == "fake" {
		if f.primaryErr != nil {
			return agentports.ProviderCapabilities{}, f.primaryErr
		}
		return agentports.ProviderCapabilities{RepairSourceCandidates: f.candidates, HardRuntimeDeadline: true, MaxRuntimeSeconds: 600}, nil
	}
	if providerID == "recovery-cli" {
		if f.recoveryErr != nil {
			return agentports.ProviderCapabilities{}, f.recoveryErr
		}
		if !f.recoveryHealth {
			return agentports.ProviderCapabilities{}, agentdomain.ErrProviderUnavailable
		}
		return agentports.ProviderCapabilities{RepairSourceCandidates: true, HardRuntimeDeadline: true, MaxRuntimeSeconds: 600}, nil
	}
	return agentports.ProviderCapabilities{}, agentdomain.ErrNotFound
}

func newRecoveryRouter(agents *fakeAgents, providers *recoveryProviders, projects *fakeProjects, recovery string) (*TaskRouter, error) {
	return NewTaskRouterWithRecovery(agents, projects, &fakePolicies{}, providers, fakeCredentials{}, stubContextVerifier{}, "fake", recovery)
}

func repairInput() agentapp.SubmitInput {
	return agentapp.SubmitInput{OwnerUserID: "0198d7ea-2110-7c42-b659-c5e4d73bc342", ProjectID: "0198d7ea-2110-7c42-b659-c5e4d73bc343", IdempotencyKey: "recovery-key", Payload: []byte(`{"goal":"repair"}`), RepairSources: true}
}

func boundProject(provider string) *fakeProjects {
	return &fakeProjects{project: projectdomain.Project{HarnessBinding: &projectdomain.HarnessBinding{ProviderID: provider}}}
}

// A healthy project harness with the repair-candidate capability stays the
// repair's provider; the recovery tier is never consulted.
func TestRecoveryRoutingPrefersHealthyProjectHarness(t *testing.T) {
	agents := &fakeAgents{}
	providers := &recoveryProviders{candidates: true, recoveryHealth: true}
	router, err := newRecoveryRouter(agents, providers, boundProject("fake"), "recovery-cli")
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.SubmitWithResult(context.Background(), repairInput())
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.ProviderID != "fake" || len(providers.lookups) != 1 {
		t.Fatalf("healthy project harness must win without fallback: %s %v", result.Task.ProviderID, providers.lookups)
	}
}

// An unhealthy project harness falls back to the configured recovery tier,
// with the same capability and credential verification applied.
func TestRecoveryRoutingFallsBackOnUnhealthyPrimary(t *testing.T) {
	agents := &fakeAgents{}
	providers := &recoveryProviders{candidates: true, primaryErr: agentdomain.ErrProviderUnavailable, recoveryHealth: true}
	router, err := newRecoveryRouter(agents, providers, boundProject("fake"), "recovery-cli")
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.SubmitWithResult(context.Background(), repairInput())
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.ProviderID != "recovery-cli" {
		t.Fatalf("unhealthy primary must fall back to recovery: %s", result.Task.ProviderID)
	}
	if result.Task.ID == "" || len(agents.submitted) != 1 {
		t.Fatal("recovery admission must create exactly one task")
	}
}

// A primary without the repair-candidate capability also falls back; the
// recovery tier itself must declare the capability.
func TestRecoveryRoutingFallsBackOnMissingCapability(t *testing.T) {
	agents := &fakeAgents{}
	providers := &recoveryProviders{candidates: false, recoveryHealth: true}
	router, err := newRecoveryRouter(agents, providers, boundProject("fake"), "recovery-cli")
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.SubmitWithResult(context.Background(), repairInput())
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.ProviderID != "recovery-cli" {
		t.Fatalf("capability-missing primary must fall back: %s", result.Task.ProviderID)
	}
}

// Both tiers unavailable ends in the precise awaiting-manual verdict with
// zero side effects.
func TestRecoveryRoutingBothTiersUnavailable(t *testing.T) {
	agents := &fakeAgents{}
	providers := &recoveryProviders{candidates: true, primaryErr: agentdomain.ErrProviderUnavailable, recoveryHealth: false}
	router, err := newRecoveryRouter(agents, providers, boundProject("fake"), "recovery-cli")
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.SubmitWithResult(context.Background(), repairInput())
	if !errors.Is(err, agentdomain.ErrRepairAwaitingManual) {
		t.Fatalf("both tiers unavailable must end awaiting manual, got %v", err)
	}
	if len(agents.submitted) != 0 {
		t.Fatal("awaiting manual must consume nothing")
	}
}

// No configured recovery tier keeps the honest fail-closed verdict; ordinary
// tasks never fall back even when a recovery provider exists.
func TestRecoveryRoutingNeverAppliesToOrdinaryTasks(t *testing.T) {
	agents := &fakeAgents{}
	providers := &recoveryProviders{candidates: false, primaryErr: agentdomain.ErrProviderUnavailable, recoveryHealth: true}
	router, err := newRecoveryRouter(agents, providers, boundProject("fake"), "recovery-cli")
	if err != nil {
		t.Fatal(err)
	}
	ordinary := repairInput()
	ordinary.RepairSources = false
	if _, err := router.SubmitWithResult(context.Background(), ordinary); !errors.Is(err, agentdomain.ErrProviderUnavailable) {
		t.Fatalf("ordinary tasks must fail closed on the bound provider, got %v", err)
	}
	if len(agents.submitted) != 0 {
		t.Fatal("ordinary task must not be admitted through recovery")
	}

	noRecovery, err := newRecoveryRouter(&fakeAgents{}, &recoveryProviders{candidates: false}, boundProject("fake"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noRecovery.SubmitWithResult(context.Background(), repairInput()); !errors.Is(err, agentdomain.ErrProviderCapabilityMissing) {
		t.Fatalf("no recovery tier must keep the capability verdict, got %v", err)
	}
}

// The recovery tier equal to the failed primary cannot "fall back" onto
// itself: the verdict is awaiting manual, not a self-retry loop.
func TestRecoveryRoutingRefusesSelfFallback(t *testing.T) {
	agents := &fakeAgents{}
	providers := &recoveryProviders{candidates: false, recoveryHealth: true}
	router, err := newRecoveryRouter(agents, providers, boundProject("fake"), "fake")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.SubmitWithResult(context.Background(), repairInput()); !errors.Is(err, agentdomain.ErrRepairAwaitingManual) {
		t.Fatalf("self fallback must end awaiting manual, got %v", err)
	}
}
