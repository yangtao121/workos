package application

import (
	"context"
	"errors"
	"fmt"
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
	approvalCalls    int
	decideCmd        *ports.DecideApprovalCommand

	approvalPage       []domain.Approval
	listApprovalsLimit int

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
	s.approvalCalls++
	return s.approvalToReturn, s.approvalErr
}

func (s *stubRepository) ListApprovals(_ context.Context, _ string, _ string, _ domain.ApprovalState, _ string, limit int) ([]domain.Approval, error) {
	s.listApprovalsLimit = limit
	if len(s.approvalPage) > limit {
		return s.approvalPage[:limit], nil
	}
	return s.approvalPage, nil
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
	return ports.ProviderCapabilities{
		HardTokenBudget: true, HardRuntimeDeadline: true, UsageReporting: true,
		MaxOutputTokens: 384_000, MaxRuntimeSeconds: 600,
	}
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
		service, err := NewApprovalService(repository, installations, providers, nil)
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
		repository := &stubRepository{approvalToReturn: base, getPolicy: domain.Policy{ProjectID: testProject, Spec: spec, Source: domain.PolicySourceExplicit, Revision: 2}, getPolicyFound: true}
		service := newTest(repository, &stubInstallations{facts: ports.InstallationFacts{GrantedPermissions: []string{"agent.task.run"}}}, &stubProviders{capabilities: completeCapabilities()})
		if _, err := service.Decide(context.Background(), input(base.ID)); err != domain.ErrApprovalNotPending {
			t.Fatalf("verdict: %v", err)
		}
		if repository.decideCmd != nil {
			t.Fatal("decision reached the store despite drifted policy")
		}
	})
	t.Run("policy project binding drifted", func(t *testing.T) {
		t.Parallel()
		repository := &stubRepository{approvalToReturn: base, getPolicy: domain.Policy{ProjectID: "018f0000-0000-7000-8000-0000000000ee", Spec: spec, Source: domain.PolicySourceExplicit, Revision: 1}, getPolicyFound: true}
		service := newTest(repository, &stubInstallations{facts: ports.InstallationFacts{GrantedPermissions: []string{"agent.task.run"}}}, &stubProviders{capabilities: completeCapabilities()})
		if _, err := service.Decide(context.Background(), input(base.ID)); !errors.Is(err, domain.ErrPolicyCorrupt) {
			t.Fatalf("verdict: %v", err)
		}
		if repository.decideCmd != nil {
			t.Fatal("decision reached the store despite corrupt project binding")
		}
	})
	t.Run("policy spec drifted without revision", func(t *testing.T) {
		t.Parallel()
		drifted := spec
		drifted.MaxOutputTokensPerTask++
		repository := &stubRepository{approvalToReturn: base, getPolicy: domain.Policy{ProjectID: testProject, Spec: drifted, Source: domain.PolicySourceExplicit, Revision: 1}, getPolicyFound: true}
		service := newTest(repository, &stubInstallations{facts: ports.InstallationFacts{GrantedPermissions: []string{"agent.task.run"}}}, &stubProviders{capabilities: completeCapabilities()})
		if _, err := service.Decide(context.Background(), input(base.ID)); err != domain.ErrApprovalNotPending {
			t.Fatalf("verdict: %v", err)
		}
		if repository.decideCmd != nil {
			t.Fatal("decision reached the store despite drifted policy spec")
		}
	})
	t.Run("provider capability lost", func(t *testing.T) {
		t.Parallel()
		repository := &stubRepository{approvalToReturn: base, getPolicy: domain.Policy{ProjectID: testProject, Spec: spec, Source: domain.PolicySourceExplicit, Revision: 1}, getPolicyFound: true}
		service := newTest(repository, &stubInstallations{facts: ports.InstallationFacts{GrantedPermissions: []string{"agent.task.run"}}}, &stubProviders{capabilities: ports.ProviderCapabilities{HardTokenBudget: true}})
		if _, err := service.Decide(context.Background(), input(base.ID)); err != domain.ErrProviderCapabilityMissing {
			t.Fatalf("verdict: %v", err)
		}
		if repository.decideCmd != nil {
			t.Fatal("decision reached the store despite lost capability")
		}
	})
	t.Run("provider maxima below the approved budget", func(t *testing.T) {
		t.Parallel()
		// A complete contract whose enforced maxima cannot hold the approval's
		// snapshot would only be refused inside the adapter — after the daily
		// reservation and the queue slot. The approve must fail closed here.
		underpowered := completeCapabilities()
		underpowered.MaxOutputTokens = spec.MaxOutputTokensPerTask - 1
		repository := &stubRepository{approvalToReturn: base, getPolicy: domain.Policy{ProjectID: testProject, Spec: spec, Source: domain.PolicySourceExplicit, Revision: 1}, getPolicyFound: true}
		service := newTest(repository, &stubInstallations{facts: ports.InstallationFacts{GrantedPermissions: []string{"agent.task.run"}}}, &stubProviders{capabilities: underpowered})
		if _, err := service.Decide(context.Background(), input(base.ID)); err != domain.ErrProviderCapabilityMissing {
			t.Fatalf("verdict: %v", err)
		}
		if repository.decideCmd != nil {
			t.Fatal("decision reached the store despite underpowered provider")
		}
	})
	t.Run("reject skips the world revalidation", func(t *testing.T) {
		t.Parallel()
		// A reject must stay possible after the installation is gone and the
		// provider is unreachable: revoking a waiting run can never depend on
		// the world the approval was created in.
		repository := &stubRepository{approvalToReturn: base}
		service := newTest(repository, &stubInstallations{err: domain.ErrNotFound}, &stubProviders{err: domain.ErrNotFound})
		reject := DecideInput{OwnerUserID: "owner", ApprovalID: base.ID, Decision: domain.ApprovalDecisionReject, IdempotencyKey: "reject-key"}
		if _, err := service.Decide(context.Background(), reject); err != nil {
			t.Fatalf("reject despite dead world: %v", err)
		}
		if repository.decideCmd == nil || repository.decideCmd.Decision != domain.ApprovalDecisionReject {
			t.Fatalf("reject not forwarded: %+v", repository.decideCmd)
		}
		if repository.decideCmd.DecisionDigest != domain.DecideApprovalRequestDigest(base.ID, domain.ApprovalDecisionReject) {
			t.Fatal("reject digest not derived")
		}
		if repository.approvalCalls != 1 || repository.policyCalls != 0 {
			t.Fatalf("reject must not revalidate the world: approvals=%d policies=%d", repository.approvalCalls, repository.policyCalls)
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
		repository := &stubRepository{approvalToReturn: base, getPolicy: domain.Policy{ProjectID: testProject, Spec: spec, Source: domain.PolicySourceExplicit, Revision: 1}, getPolicyFound: true}
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
		repository := &stubRepository{approvalToReturn: base, getPolicy: domain.Policy{ProjectID: testProject, Spec: spec, Source: domain.PolicySourceExplicit, Revision: 1}, getPolicyFound: true}
		service := newTest(repository, &stubInstallations{facts: ports.InstallationFacts{GrantedPermissions: []string{"agent.task.run"}}}, &stubProviders{capabilities: completeCapabilities()})
		input := DecideInput{OwnerUserID: "owner", ApprovalID: base.ID, Decision: domain.ApprovalDecision("maybe"), IdempotencyKey: "k"}
		if _, err := service.Decide(context.Background(), input); err == nil {
			t.Fatal("unknown decision accepted")
		}
	})
}

// TestApprovalServiceListPagination pins the paging contract: the service
// probes one row beyond the effective page size, so the next-page token is
// present exactly when a further page exists — never on a full final page —
// and the oversize clamp still probes with the clamped size.
func TestApprovalServiceListPagination(t *testing.T) {
	t.Parallel()
	newService := func(repository *stubRepository) *ApprovalService {
		service, err := NewApprovalService(repository, &stubInstallations{}, &stubProviders{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	approvals := func(count int) []domain.Approval {
		page := make([]domain.Approval, count)
		for index := range page {
			page[index] = domain.Approval{ID: fmt.Sprintf("018f0000-0000-7000-8000-%012d", index), State: domain.ApprovalPending}
		}
		return page
	}

	t.Run("default page probes one beyond and returns the token", func(t *testing.T) {
		repository := &stubRepository{approvalPage: approvals(51)}
		page, next, err := newService(repository).List(context.Background(), "owner", "", "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != 50 || next != page[49].ID {
			t.Fatalf("default page wrong: len=%d next=%q", len(page), next)
		}
		if repository.listApprovalsLimit != 51 {
			t.Fatalf("probe must fetch effective limit + 1, got %d", repository.listApprovalsLimit)
		}
	})
	t.Run("full final page yields no phantom token", func(t *testing.T) {
		repository := &stubRepository{approvalPage: approvals(50)}
		page, next, err := newService(repository).List(context.Background(), "owner", "", "", "", 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != 50 || next != "" {
			t.Fatalf("final full page must not advertise a next page: len=%d next=%q", len(page), next)
		}
	})
	t.Run("oversize request is clamped before the probe", func(t *testing.T) {
		repository := &stubRepository{approvalPage: approvals(101)}
		page, next, err := newService(repository).List(context.Background(), "owner", "", "", "", 150)
		if err != nil {
			t.Fatal(err)
		}
		if repository.listApprovalsLimit != 101 {
			t.Fatalf("oversize clamp must probe 101, got %d", repository.listApprovalsLimit)
		}
		if len(page) != 100 || next != page[99].ID {
			t.Fatalf("clamped page wrong: len=%d next=%q", len(page), next)
		}
	})
}
