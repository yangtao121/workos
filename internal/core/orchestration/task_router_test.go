package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

type fakePolicies struct {
	policy agentdomain.Policy
	err    error
}

func (f *fakePolicies) EffectivePolicy(context.Context, string, string, string) (agentdomain.Policy, error) {
	if f.err != nil {
		return agentdomain.Policy{}, f.err
	}
	policy := f.policy
	if policy.Spec.Mode == "" {
		policy = agentdomain.SystemDefaultPolicy()
	}
	return policy, nil
}

type fakeProviders struct {
	capabilities agentports.ProviderCapabilities
	err          error
	lookups      int
}

func (f *fakeProviders) Capabilities(_ context.Context, providerID string) (agentports.ProviderCapabilities, error) {
	f.lookups++
	if f.err != nil {
		return agentports.ProviderCapabilities{}, f.err
	}
	if providerID == "unknown" {
		return agentports.ProviderCapabilities{}, agentdomain.ErrNotFound
	}
	capabilities := f.capabilities
	if !capabilities.UsageReporting && !capabilities.HardRuntimeDeadline && !capabilities.HardTokenBudget {
		capabilities = agentports.ProviderCapabilities{
			HardTokenBudget: true, HardRuntimeDeadline: true, UsageReporting: true,
			MaxOutputTokens: 384_000, MaxRuntimeSeconds: 600,
		}
	}
	return capabilities, nil
}

// newTestRouter wires the default allow-policy adjudication fakes.
func newTestRouter(agents *fakeAgents, projects *fakeProjects) (*TaskRouter, error) {
	return NewTaskRouter(agents, projects, &fakePolicies{}, &fakeProviders{}, "fake")
}

type fakeAgents struct {
	existing               *agentdomain.Task
	submitted              []agentapp.SubmitInput
	appSubmitted           []agentapp.AppSubmitInput
	appApprovalSubmitted   []agentapp.AppSubmitInput
	appReplayTask          *agentdomain.Task
	appReplayDigestDiffers bool
}

func (f *fakeAgents) GetByIdempotency(context.Context, string, string) (agentdomain.Task, error) {
	if f.existing == nil {
		return agentdomain.Task{}, agentdomain.ErrNotFound
	}
	return *f.existing, nil
}

func (f *fakeAgents) Submit(_ context.Context, input agentapp.SubmitInput) (agentdomain.Task, error) {
	f.submitted = append(f.submitted, input)
	return agentdomain.Task{ID: "new-task", ProviderID: input.ProviderID}, nil
}

func (f *fakeAgents) SubmitForApp(_ context.Context, input agentapp.AppSubmitInput) (agentdomain.Task, error) {
	f.appSubmitted = append(f.appSubmitted, input)
	return agentdomain.Task{ID: "new-app-task", ProviderID: input.ProviderID}, nil
}

func (f *fakeAgents) SubmitForAppApproval(_ context.Context, input agentapp.AppSubmitInput) (agentdomain.Task, agentdomain.Approval, error) {
	f.appApprovalSubmitted = append(f.appApprovalSubmitted, input)
	return agentdomain.Task{ID: "waiting-app-task", ProviderID: input.ProviderID}, agentdomain.Approval{ID: "approval-1", TaskID: "waiting-app-task"}, nil
}

func (f *fakeAgents) GetAppTaskByIdempotency(context.Context, string, string, string) (agentdomain.Task, string, bool, error) {
	if f.appReplayDigestDiffers {
		return agentdomain.Task{}, "sha256:different", true, nil
	}
	if f.appReplayTask != nil {
		return *f.appReplayTask, agentdomain.AppTaskRequestDigest("role", "goal"), true, nil
	}
	return agentdomain.Task{}, "", false, nil
}

func (f *fakeAgents) GetAppTask(context.Context, string, string, string) (agentdomain.Task, string, error) {
	return agentdomain.Task{}, "", agentdomain.ErrNotFound
}

func (f *fakeAgents) AppTaskEvents(context.Context, string, string, string, int64, int) ([]agentdomain.Event, error) {
	return nil, agentdomain.ErrNotFound
}

type fakeProjects struct {
	project       projectdomain.Project
	err           error
	gets          int
	lastOwnerID   string
	lastProjectID string
}

func (f *fakeProjects) Get(_ context.Context, ownerID, projectID string) (projectdomain.Project, error) {
	f.gets++
	f.lastOwnerID, f.lastProjectID = ownerID, projectID
	return f.project, f.err
}

func TestTaskRouterResolvesProviderSnapshots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		projectID string
		project   projectdomain.Project
		want      string
	}{
		{name: "global default", want: "fake"},
		{name: "project without binding", projectID: "project-1", want: "fake"},
		{
			name: "project binding", projectID: "project-1", want: "deepseek",
			project: projectdomain.Project{HarnessBinding: &projectdomain.HarnessBinding{ProviderID: "deepseek"}},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			agents := &fakeAgents{}
			projects := &fakeProjects{project: test.project}
			router, err := newTestRouter(agents, projects)
			if err != nil {
				t.Fatal(err)
			}
			task, err := router.Submit(context.Background(), agentapp.SubmitInput{
				OwnerUserID: "owner", IdempotencyKey: "key", ProjectID: test.projectID, Payload: []byte(`{"goal":"test"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if task.ProviderID != test.want || len(agents.submitted) != 1 || agents.submitted[0].ProviderID != test.want {
				t.Fatalf("provider snapshot = %q, submitted = %#v", task.ProviderID, agents.submitted)
			}
			if test.projectID != "" && (projects.lastOwnerID != "owner" || projects.lastProjectID != test.projectID) {
				t.Fatalf("project lookup was not owner scoped: owner=%q project=%q", projects.lastOwnerID, projects.lastProjectID)
			}
		})
	}
}

func TestTaskRouterReturnsIdempotentTaskBeforeProjectLookup(t *testing.T) {
	t.Parallel()
	existing := agentdomain.Task{ID: "existing", ProviderID: "deepseek"}
	agents := &fakeAgents{existing: &existing}
	projects := &fakeProjects{
		project: projectdomain.Project{HarnessBinding: &projectdomain.HarnessBinding{ProviderID: "fake"}},
	}
	router, err := newTestRouter(agents, projects)
	if err != nil {
		t.Fatal(err)
	}
	task, err := router.Submit(context.Background(), agentapp.SubmitInput{
		OwnerUserID: "owner", IdempotencyKey: "same-key", ProjectID: "project-1", Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != existing.ID || task.ProviderID != "deepseek" || projects.gets != 0 || len(agents.submitted) != 0 {
		t.Fatalf("idempotent route changed: task=%#v project_gets=%d submits=%d", task, projects.gets, len(agents.submitted))
	}
}

func TestTaskRouterRejectsInaccessibleOrArchivedProject(t *testing.T) {
	t.Parallel()
	archivedAt := time.Now().UTC()
	tests := []struct {
		name     string
		projects *fakeProjects
	}{
		{name: "missing", projects: &fakeProjects{err: projectdomain.ErrNotFound}},
		{name: "invalid scope", projects: &fakeProjects{err: projectdomain.ErrInvalid}},
		{name: "archived", projects: &fakeProjects{project: projectdomain.Project{ArchivedAt: &archivedAt}}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			router, err := newTestRouter(&fakeAgents{}, test.projects)
			if err != nil {
				t.Fatal(err)
			}
			_, err = router.Submit(context.Background(), agentapp.SubmitInput{
				OwnerUserID: "owner", IdempotencyKey: "key", ProjectID: "project-1", Payload: []byte(`{}`),
			})
			if !errors.Is(err, agentdomain.ErrProjectDenied) {
				t.Fatalf("expected project denial, got %v", err)
			}
		})
	}
}

func TestTaskRouterSubmitForAppReplaysBeforeProjectLookup(t *testing.T) {
	t.Parallel()
	first := agentdomain.Task{ID: "first-app-task", ProviderID: "bound"}
	agents := &fakeAgents{appReplayTask: &first}
	projects := &fakeProjects{}
	router, err := newTestRouter(agents, projects)
	if err != nil {
		t.Fatal(err)
	}
	task, err := router.SubmitForApp(context.Background(), agentapp.AppSubmitInput{
		OwnerUserID: "owner-1", AppInstanceID: "instance-1", ClientIdempotencyKey: "key-1",
		RequestDigest: agentdomain.AppTaskRequestDigest("role", "goal"),
		ProjectID:     "project-1", Role: "role", Goal: "goal",
	})
	if err != nil || task.ID != "first-app-task" || task.ProviderID != "bound" {
		t.Fatalf("replay failed: %v %+v", err, task)
	}
	if len(agents.appSubmitted) != 0 {
		t.Fatal("replay must not resubmit")
	}
	if projects.gets != 0 {
		t.Fatal("replay must not consult the project")
	}
}

func TestTaskRouterSubmitForAppConflictsOnDifferentRequest(t *testing.T) {
	t.Parallel()
	agents := &fakeAgents{appReplayDigestDiffers: true}
	router, err := newTestRouter(agents, &fakeProjects{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.SubmitForApp(context.Background(), agentapp.AppSubmitInput{
		OwnerUserID: "owner-1", AppInstanceID: "instance-1", ClientIdempotencyKey: "key-1",
		RequestDigest: agentdomain.AppTaskRequestDigest("role", "goal"),
		ProjectID:     "project-1", Role: "role", Goal: "goal",
	})
	if !errors.Is(err, agentdomain.ErrIdempotencyConflict) {
		t.Fatalf("different-request verdict: %v", err)
	}
}

func TestTaskRouterSubmitForAppSnapshotsProjectProvider(t *testing.T) {
	t.Parallel()
	agents := &fakeAgents{}
	projects := &fakeProjects{project: projectdomain.Project{
		ID: "project-1", OwnerUserID: "owner-1",
		HarnessBinding: &projectdomain.HarnessBinding{ProviderID: "deepseek"},
	}}
	router, err := newTestRouter(agents, projects)
	if err != nil {
		t.Fatal(err)
	}
	task, err := router.SubmitForApp(context.Background(), agentapp.AppSubmitInput{
		OwnerUserID: "owner-1", AppInstanceID: "instance-1", ClientIdempotencyKey: "key-1",
		RequestDigest: agentdomain.AppTaskRequestDigest("role", "goal"),
		ProjectID:     "project-1", Role: "role", Goal: "goal",
	})
	if err != nil || task.ProviderID != "deepseek" {
		t.Fatalf("provider snapshot missing: %v %+v", err, task)
	}
	if len(agents.appSubmitted) != 1 || agents.appSubmitted[0].ProviderID != "deepseek" {
		t.Fatalf("submission missing provider snapshot: %+v", agents.appSubmitted)
	}
	if agents.appSubmitted[0].RequestDigest != agentdomain.AppTaskRequestDigest("role", "goal") {
		t.Fatal("digest not forwarded")
	}
	if agents.appSubmitted[0].Enforcement.Policy.Source != agentdomain.PolicySourceSystemDefault ||
		agents.appSubmitted[0].Enforcement.MaxOutputTokensTask != 4096 ||
		agents.appSubmitted[0].Enforcement.Daily.MaxTasks != 50 {
		t.Fatalf("policy enforcement missing: %+v", agents.appSubmitted[0].Enforcement)
	}
}

func TestTaskRouterSubmitForAppRoutesRequireApprovalMode(t *testing.T) {
	t.Parallel()
	agents := &fakeAgents{}
	policies := &fakePolicies{policy: agentdomain.Policy{
		Spec: agentdomain.PolicySpec{
			Mode: agentdomain.PolicyModeRequireApproval, MaxOutputTokensPerTask: 128,
			MaxRuntimeSecondsPerTask: 60, MaxTasksPerUTCDay: 3, MaxReservedOutputTokensPerUTCDay: 384,
		},
		Source: agentdomain.PolicySourceExplicit, Revision: 2,
	}}
	router, err := NewTaskRouter(agents, &fakeProjects{}, policies, &fakeProviders{}, "fake")
	if err != nil {
		t.Fatal(err)
	}
	task, err := router.SubmitForApp(context.Background(), agentapp.AppSubmitInput{
		OwnerUserID: "owner-1", AppInstanceID: "instance-1", ClientIdempotencyKey: "key-1",
		RequestDigest: agentdomain.AppTaskRequestDigest("role", "goal"),
		ProjectID:     "project-1", Role: "role", Goal: "goal",
	})
	if err != nil || task.ID != "waiting-app-task" {
		t.Fatalf("approval handoff failed: %v %+v", err, task)
	}
	if len(agents.appApprovalSubmitted) != 1 || len(agents.appSubmitted) != 0 {
		t.Fatalf("mode routed to the wrong enqueue path: allow=%d approval=%d", len(agents.appSubmitted), len(agents.appApprovalSubmitted))
	}
	if agents.appApprovalSubmitted[0].Enforcement.MaxOutputTokensTask != 128 {
		t.Fatalf("approval enforcement missing: %+v", agents.appApprovalSubmitted[0].Enforcement)
	}
}

func TestTaskRouterSubmitForAppBlockModeFailsClosed(t *testing.T) {
	t.Parallel()
	agents := &fakeAgents{}
	policies := &fakePolicies{policy: agentdomain.Policy{
		Spec: agentdomain.PolicySpec{
			Mode: agentdomain.PolicyModeBlock, MaxOutputTokensPerTask: 128,
			MaxRuntimeSecondsPerTask: 60, MaxTasksPerUTCDay: 3, MaxReservedOutputTokensPerUTCDay: 384,
		},
		Source: agentdomain.PolicySourceExplicit, Revision: 2,
	}}
	router, err := NewTaskRouter(agents, &fakeProjects{}, policies, &fakeProviders{}, "fake")
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.SubmitForApp(context.Background(), agentapp.AppSubmitInput{
		OwnerUserID: "owner-1", AppInstanceID: "instance-1", ClientIdempotencyKey: "key-1",
		RequestDigest: agentdomain.AppTaskRequestDigest("role", "goal"),
		ProjectID:     "project-1", Role: "role", Goal: "goal",
	})
	if !errors.Is(err, agentdomain.ErrPolicyBlocksRuns) {
		t.Fatalf("block mode verdict: %v", err)
	}
	if len(agents.appSubmitted) != 0 || len(agents.appApprovalSubmitted) != 0 {
		t.Fatal("block mode must not enqueue anything")
	}
}

func TestTaskRouterSubmitForAppRejectsMissingProviderBudgetContract(t *testing.T) {
	t.Parallel()
	agents := &fakeAgents{}
	router, err := NewTaskRouter(agents, &fakeProjects{}, &fakePolicies{}, &fakeProviders{}, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.SubmitForApp(context.Background(), agentapp.AppSubmitInput{
		OwnerUserID: "owner-1", AppInstanceID: "instance-1", ClientIdempotencyKey: "key-1",
		RequestDigest: agentdomain.AppTaskRequestDigest("role", "goal"),
		ProjectID:     "project-1", Role: "role", Goal: "goal",
	})
	if !errors.Is(err, agentdomain.ErrProviderCapabilityMissing) {
		t.Fatalf("provider capability verdict: %v", err)
	}
	partial := &fakeProviders{capabilities: agentports.ProviderCapabilities{HardTokenBudget: true, UsageReporting: true}}
	router, err = NewTaskRouter(agents, &fakeProjects{}, &fakePolicies{}, partial, "fake")
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.SubmitForApp(context.Background(), agentapp.AppSubmitInput{
		OwnerUserID: "owner-1", AppInstanceID: "instance-1", ClientIdempotencyKey: "key-2",
		RequestDigest: agentdomain.AppTaskRequestDigest("role", "goal"),
		ProjectID:     "project-1", Role: "role", Goal: "goal",
	})
	if !errors.Is(err, agentdomain.ErrProviderCapabilityMissing) {
		t.Fatalf("partial capability verdict: %v", err)
	}
	// A complete contract with maxima below the policy budget is equally
	// unusable: the adapter would only refuse the run after the queue slot
	// and the daily reservation were already spent.
	underpowered := &fakeProviders{capabilities: agentports.ProviderCapabilities{
		HardTokenBudget: true, HardRuntimeDeadline: true, UsageReporting: true,
		MaxOutputTokens: 4_096, MaxRuntimeSeconds: 600,
	}}
	policy := agentdomain.SystemDefaultPolicy()
	policy.Spec.MaxOutputTokensPerTask = 8_192
	router, err = NewTaskRouter(agents, &fakeProjects{}, &fixedPolicies{policy: policy}, underpowered, "fake")
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.SubmitForApp(context.Background(), agentapp.AppSubmitInput{
		OwnerUserID: "owner-1", AppInstanceID: "instance-1", ClientIdempotencyKey: "key-3",
		RequestDigest: agentdomain.AppTaskRequestDigest("role", "goal"),
		ProjectID:     "project-1", Role: "role", Goal: "goal",
	})
	if !errors.Is(err, agentdomain.ErrProviderCapabilityMissing) {
		t.Fatalf("over-budget policy verdict: %v", err)
	}
	if len(agents.appSubmitted) != 0 {
		t.Fatal("capability-missing runs must not enqueue")
	}
}

// fixedPolicies serves one explicit policy regardless of the installation.
type fixedPolicies struct {
	policy agentdomain.Policy
}

func (f *fixedPolicies) EffectivePolicy(context.Context, string, string, string) (agentdomain.Policy, error) {
	return f.policy, nil
}
