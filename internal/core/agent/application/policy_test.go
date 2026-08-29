package application

import (
	"context"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/agent/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// stubRepository records the calls the application layer makes and answers
// with canned facts.
type stubRepository struct {
	policyCalls int

	getPolicyFound bool
	getPolicy      domain.Policy
	setPolicyErr   error
	setPolicy      domain.Policy
	setPolicyCmd   ports.SetPolicyCommand

	approvalToReturn domain.Approval
	approvalErr      error
	decideCmd        *ports.DecideApprovalCommand

	usage ports.DailyUsage
}

func (s *stubRepository) Create(context.Context, domain.Task, string) (domain.Task, error) {
	return domain.Task{}, nil
}

func (s *stubRepository) CreateForApp(context.Context, domain.Task, ports.AppTaskProvenance, ports.PolicySnapshot, ports.DailyAllowance) (domain.Task, error) {
	return domain.Task{}, nil
}

func (s *stubRepository) CreateForAppApproval(context.Context, domain.Task, domain.Approval, ports.AppTaskProvenance) (domain.Task, domain.Approval, error) {
	return domain.Task{}, domain.Approval{}, nil
}

func (s *stubRepository) Get(context.Context, string, string) (domain.Task, error) {
	return domain.Task{}, domain.ErrNotFound
}

func (s *stubRepository) GetByIdempotency(context.Context, string, string) (domain.Task, error) {
	return domain.Task{}, domain.ErrNotFound
}

func (s *stubRepository) GetAppTaskRequest(context.Context, string, string, string) (ports.AppTaskRequestRecord, bool, error) {
	return ports.AppTaskRequestRecord{}, false, nil
}

func (s *stubRepository) GetAppTaskByTask(context.Context, string, string, string) (ports.AppTaskRequestRecord, bool, error) {
	return ports.AppTaskRequestRecord{}, false, nil
}

func (s *stubRepository) List(context.Context, string, string, string, int) ([]domain.Task, error) {
	return nil, nil
}

func (s *stubRepository) Cancel(context.Context, string, string, string, time.Time) (domain.Task, *domain.Event, error) {
	return domain.Task{}, nil, nil
}

func (s *stubRepository) ListEvents(context.Context, string, string, int64, int) ([]domain.Event, error) {
	return nil, nil
}

func (s *stubRepository) Claim(context.Context, string, time.Duration, string, time.Time) (*domain.Lease, error) {
	return nil, nil
}

func (s *stubRepository) Renew(context.Context, string, string, time.Duration, time.Time) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (s *stubRepository) AppendEvent(context.Context, string, string, domain.Event, domain.State, string, string, *domain.UsageReport, time.Time) (domain.Event, error) {
	return domain.Event{}, nil
}

func (s *stubRepository) FinishLease(context.Context, string, string, time.Time) error { return nil }

func (s *stubRepository) GetPolicy(context.Context, string, string) (domain.Policy, bool, error) {
	return s.getPolicy, s.getPolicyFound, nil
}

func (s *stubRepository) SetPolicy(_ context.Context, command ports.SetPolicyCommand) (domain.Policy, ports.SetPolicyResult, error) {
	s.policyCalls++
	s.setPolicyCmd = command
	if s.setPolicyErr != nil {
		return domain.Policy{}, ports.SetPolicyResult{}, s.setPolicyErr
	}
	return s.setPolicy, ports.SetPolicyResult{Changed: true}, nil
}

func (s *stubRepository) GetPolicyRequest(context.Context, string, string) (ports.PolicyRequestRecord, bool, error) {
	return ports.PolicyRequestRecord{}, false, nil
}

func (s *stubRepository) GetApproval(context.Context, string, string) (domain.Approval, error) {
	return s.approvalToReturn, s.approvalErr
}

func (s *stubRepository) ListApprovals(context.Context, string, string, domain.ApprovalState, string, int) ([]domain.Approval, error) {
	return nil, nil
}

func (s *stubRepository) DecideApproval(_ context.Context, command ports.DecideApprovalCommand) (domain.Approval, error) {
	s.decideCmd = &command
	return s.approvalToReturn, nil
}

func (s *stubRepository) GetAppDailyUsage(context.Context, string, string, string) (ports.DailyUsage, error) {
	return s.usage, nil
}

type stubInstallations struct {
	facts ports.InstallationFacts
	err   error
	calls int
}

func (s *stubInstallations) ResolveActiveInstallation(context.Context, string, string, string) (ports.InstallationFacts, error) {
	s.calls++
	if s.err != nil {
		return ports.InstallationFacts{}, s.err
	}
	return s.facts, nil
}

type stubProviders struct {
	capabilities ports.ProviderCapabilities
	err          error
	calls        int
}

func (s *stubProviders) Capabilities(context.Context, string) (ports.ProviderCapabilities, error) {
	s.calls++
	if s.err != nil {
		return ports.ProviderCapabilities{}, s.err
	}
	return s.capabilities, nil
}

const testProject = "018f0000-0000-7000-8000-0000000000ab"
const testInstance = "018f0000-0000-7000-8000-0000000000cd"

func completeCapabilities() ports.ProviderCapabilities {
	return ports.ProviderCapabilities{HardTokenBudget: true, HardRuntimeDeadline: true, UsageReporting: true}
}

func TestPolicyServiceSetPolicyValidatesBeforeTouchingFacts(t *testing.T) {
	t.Parallel()
	repository := &stubRepository{}
	installations := &stubInstallations{}
	service, err := NewPolicyService(repository, installations, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	badSpec := domain.PolicySpec{Mode: domain.PolicyModeAllow, MaxOutputTokensPerTask: 0}
	_, _, err = service.SetPolicy(context.Background(), SetPolicyInput{
		OwnerUserID: "owner", ProjectID: testProject, AppInstanceID: testInstance,
		Spec: badSpec, IdempotencyKey: "key",
	})
	if err == nil {
		t.Fatal("invalid spec accepted")
	}
	if repository.policyCalls != 0 || installations.calls != 0 {
		t.Fatalf("invalid input reached facts: repo=%d install=%d", repository.policyCalls, installations.calls)
	}
	_, _, err = service.SetPolicy(context.Background(), SetPolicyInput{
		OwnerUserID: "owner", ProjectID: "not-a-uuid", AppInstanceID: testInstance,
		Spec: validAppPolicySpec(), IdempotencyKey: "key",
	})
	if err == nil {
		t.Fatal("invalid uuid accepted")
	}
}

func TestPolicyServiceSetPolicyRevalidatesInstallationLiveness(t *testing.T) {
	t.Parallel()
	repository := &stubRepository{}
	installations := &stubInstallations{err: domain.ErrNotFound}
	service, err := NewPolicyService(repository, installations, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.SetPolicy(context.Background(), SetPolicyInput{
		OwnerUserID: "owner", ProjectID: testProject, AppInstanceID: testInstance,
		Spec: validAppPolicySpec(), IdempotencyKey: "key",
	})
	if err == nil {
		t.Fatal("dead installation accepted")
	}
	if repository.policyCalls != 0 {
		t.Fatal("mutation reached the store despite dead installation")
	}
}

func TestPolicyServiceEffectivePolicyFallsBackToSystemDefault(t *testing.T) {
	t.Parallel()
	repository := &stubRepository{}
	installations := &stubInstallations{facts: ports.InstallationFacts{AppID: "fixture.app"}}
	service, err := NewPolicyService(repository, installations, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := service.EffectivePolicy(context.Background(), "owner", testProject, testInstance)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Source != domain.PolicySourceSystemDefault || policy.AppID != "fixture.app" || policy.Revision != domain.SystemDefaultPolicyVersion {
		t.Fatalf("unexpected default projection: %#v", policy)
	}
	if err := policy.Spec.Validate(); err != nil {
		t.Fatalf("default must stay finite: %v", err)
	}
}

func validAppPolicySpec() domain.PolicySpec {
	return domain.PolicySpec{
		Mode: domain.PolicyModeRequireApproval, MaxOutputTokensPerTask: 128, MaxRuntimeSecondsPerTask: 60,
		MaxTasksPerUTCDay: 3, MaxReservedOutputTokensPerUTCDay: 384,
	}
}

func TestApprovalServiceDecideFailsClosedOnDriftedWorld(t *testing.T) {
	spec := validAppPolicySpec()
	base := domain.Approval{
		ID: "018f0000-0000-7000-8000-0000000000ef", OwnerUserID: "owner",
		AppInstanceID: testInstance, ProjectID: testProject, TaskID: "018f0000-0000-7000-8000-000000000099",
		GoalExcerpt: "goal", ProviderID: "fake", Source: domain.PolicySourceExplicit,
		Spec: spec, Revision: 1, State: domain.ApprovalPending,
	}
	newTest := func(repository *stubRepository, installations *stubInstallations, providers *stubProviders) *ApprovalService {
		service, err := NewApprovalService(repository, installations, providers)
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	input := func(approvalID string) DecideInput {
		return DecideInput{OwnerUserID: "owner", ApprovalID: approvalID, Decision: domain.ApprovalDecisionApprove, IdempotencyKey: "decision-key"}
	}

	t.Run("installation gone", func(t *testing.T) {
		t.Parallel()
		repository := &stubRepository{approvalToReturn: base}
		service := newTest(repository, &stubInstallations{err: domain.ErrNotFound}, &stubProviders{capabilities: completeCapabilities()})
		if _, err := service.Decide(context.Background(), input(base.ID)); err != domain.ErrApprovalNotPending {
			t.Fatalf("verdict: %v", err)
		}
		if repository.decideCmd != nil {
			t.Fatal("decision reached the store despite dead installation")
		}
	})
	t.Run("grant revoked", func(t *testing.T) {
		t.Parallel()
		repository := &stubRepository{approvalToReturn: base}
		installations := &stubInstallations{facts: ports.InstallationFacts{GrantedPermissions: []string{"agent.event.watch"}}}
		service := newTest(repository, installations, &stubProviders{capabilities: completeCapabilities()})
		if _, err := service.Decide(context.Background(), input(base.ID)); err != domain.ErrApprovalNotPending {
			t.Fatalf("verdict: %v", err)
		}
		if repository.decideCmd != nil {
			t.Fatal("decision reached the store despite revoked grant")
		}
	})
	t.Run("policy revision drifted", func(t *testing.T) {
		t.Parallel()
		repository := &stubRepository{approvalToReturn: base, getPolicy: domain.Policy{Spec: spec, Source: domain.PolicySourceExplicit, Revision: 2}, getPolicyFound: true}
		service := newTest(repository, &stubInstallations{facts: ports.InstallationFacts{GrantedPermissions: []string{"agent.task.run"}}}, &stubProviders{capabilities: completeCapabilities()})
		if _, err := service.Decide(context.Background(), input(base.ID)); err != domain.ErrApprovalNotPending {
			t.Fatalf("verdict: %v", err)
		}
		if repository.decideCmd != nil {
			t.Fatal("decision reached the store despite drifted policy")
		}
	})
	t.Run("provider capability lost", func(t *testing.T) {
		t.Parallel()
		repository := &stubRepository{approvalToReturn: base, getPolicy: domain.Policy{Spec: spec, Source: domain.PolicySourceExplicit, Revision: 1}, getPolicyFound: true}
		service := newTest(repository, &stubInstallations{facts: ports.InstallationFacts{GrantedPermissions: []string{"agent.task.run"}}}, &stubProviders{capabilities: ports.ProviderCapabilities{HardTokenBudget: true}})
		if _, err := service.Decide(context.Background(), input(base.ID)); err != domain.ErrProviderCapabilityMissing {
			t.Fatalf("verdict: %v", err)
		}
		if repository.decideCmd != nil {
			t.Fatal("decision reached the store despite lost capability")
		}
	})
	t.Run("expired approval short-circuits", func(t *testing.T) {
		t.Parallel()
		expired := base
		expired.State = domain.ApprovalExpired
		repository := &stubRepository{approvalToReturn: expired}
		service := newTest(repository, &stubInstallations{}, &stubProviders{})
		if _, err := service.Decide(context.Background(), input(base.ID)); err != domain.ErrApprovalNotPending {
			t.Fatalf("verdict: %v", err)
		}
		if repository.decideCmd != nil {
			t.Fatal("expired approval reached the decide transaction")
		}
	})
	t.Run("healthy world reaches the transaction", func(t *testing.T) {
		t.Parallel()
		repository := &stubRepository{approvalToReturn: base, getPolicy: domain.Policy{Spec: spec, Source: domain.PolicySourceExplicit, Revision: 1}, getPolicyFound: true}
		service := newTest(repository, &stubInstallations{facts: ports.InstallationFacts{GrantedPermissions: []string{"agent.task.run", "agent.event.watch"}}}, &stubProviders{capabilities: completeCapabilities()})
		if _, err := service.Decide(context.Background(), input(base.ID)); err != nil {
			t.Fatalf("decide failed: %v", err)
		}
		if repository.decideCmd == nil || repository.decideCmd.Decision != domain.ApprovalDecisionApprove {
			t.Fatalf("decision not forwarded: %+v", repository.decideCmd)
		}
		if repository.decideCmd.DecisionDigest != domain.DecideApprovalRequestDigest(base.ID, domain.ApprovalDecisionApprove) {
			t.Fatal("decision digest not derived")
		}
	})
	t.Run("rejected decision unknown provider", func(t *testing.T) {
		t.Parallel()
		repository := &stubRepository{approvalToReturn: base, getPolicy: domain.Policy{Spec: spec, Source: domain.PolicySourceExplicit, Revision: 1}, getPolicyFound: true}
		service := newTest(repository, &stubInstallations{facts: ports.InstallationFacts{GrantedPermissions: []string{"agent.task.run"}}}, &stubProviders{capabilities: completeCapabilities()})
		input := DecideInput{OwnerUserID: "owner", ApprovalID: base.ID, Decision: domain.ApprovalDecision("maybe"), IdempotencyKey: "k"}
		if _, err := service.Decide(context.Background(), input); err == nil {
			t.Fatal("unknown decision accepted")
		}
	})
}
