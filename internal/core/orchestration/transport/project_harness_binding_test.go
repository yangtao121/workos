package transport

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	catalogdomain "github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type projectsFake struct {
	project projectdomain.Project
	owner   string
	update  projectapp.UpdateInput
}

func (f *projectsFake) Get(_ context.Context, ownerID, _ string) (projectdomain.Project, error) {
	f.owner = ownerID
	return f.project, nil
}

func (f *projectsFake) Update(_ context.Context, input projectapp.UpdateInput) (projectdomain.Project, error) {
	f.update = input
	f.project.HarnessBinding = input.HarnessBinding
	f.project.Revision = input.ExpectedRevision + 1
	return f.project, nil
}

type catalogFake struct{ catalog catalogdomain.Catalog }

func (f catalogFake) Get(context.Context) (catalogdomain.Catalog, error) { return f.catalog, nil }

func TestBindingTransportUsesIdentityAndReturnsServerPreset(t *testing.T) {
	t.Parallel()
	projects := &projectsFake{project: projectdomain.Project{ID: "project-1", OwnerUserID: "owner-1", Revision: 4}}
	binder, err := orchestration.NewProjectHarnessBinder(projects, catalogFake{catalog: catalogdomain.Catalog{
		Providers: []catalogdomain.Provider{{ID: "degraded", Health: catalogdomain.HealthDegraded}},
	}}, orchestration.BindingPreset{InstancePolicy: "ephemeral", ResourcePolicyID: "project-no-tools"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithContext(context.Background(), identity.Identity{UserID: "owner-1", DeviceID: "device-1"})
	response, err := New(binder).SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: "project-1", ExpectedRevision: 4,
		Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "degraded"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	binding := response.Msg.GetProject().GetHarnessBinding()
	if projects.owner != "owner-1" || projects.update.OwnerUserID != "owner-1" || response.Msg.GetProject().GetRevision() != 5 || binding.GetProviderId() != "degraded" || binding.GetCredentialRef() != "" || binding.GetResourcePolicyId() != "project-no-tools" {
		t.Fatalf("unexpected public binding response: %#v update=%#v", response.Msg.GetProject(), projects.update)
	}
}

func TestBindingTransportRequiresIdentityAndExplicitSelection(t *testing.T) {
	t.Parallel()
	projects := &projectsFake{project: projectdomain.Project{ID: "project-1", Revision: 1}}
	binder, err := orchestration.NewProjectHarnessBinder(projects, catalogFake{}, orchestration.BindingPreset{
		InstancePolicy: "ephemeral", ResourcePolicyID: "project-no-tools",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(binder)
	request := connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{ProjectId: "project-1", ExpectedRevision: 1})
	if _, err := handler.SetProjectHarnessBinding(context.Background(), request); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("missing identity returned %v", err)
	}
	ctx := identity.WithContext(context.Background(), identity.Identity{UserID: "owner-1", DeviceID: "device-1"})
	if _, err := handler.SetProjectHarnessBinding(ctx, request); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing selection returned %v", err)
	}
}

func TestBindingTransportUsesSafeCanonicalErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		err  error
		code connect.Code
	}{
		{context.Canceled, connect.CodeCanceled},
		{context.DeadlineExceeded, connect.CodeDeadlineExceeded},
		{projectdomain.ErrInvalid, connect.CodeInvalidArgument},
		{projectdomain.ErrNotFound, connect.CodeNotFound},
		{projectdomain.ErrConflict, connect.CodeAborted},
		{orchestration.ErrProviderUnknown, connect.CodeFailedPrecondition},
		{orchestration.ErrProviderNotSelectable, connect.CodeFailedPrecondition},
		{catalogdomain.ErrUnavailable, connect.CodeUnavailable},
		{errors.New("private-host Authorization Bearer secret"), connect.CodeInternal},
	} {
		got := mapError(test.err)
		if connect.CodeOf(got) != test.code || strings.Contains(got.Error(), "private-host") || strings.Contains(got.Error(), "Bearer") || strings.Contains(got.Error(), "secret") {
			t.Fatalf("unsafe binding error mapping: code=%s err=%v", connect.CodeOf(got), got)
		}
	}
}

func TestPublicBindingCommandHasNoPolicyOrCredentialInput(t *testing.T) {
	t.Parallel()
	request := projectv1.File_workos_project_v1_harness_binding_proto.Messages().ByName("SetProjectHarnessBindingRequest")
	service := projectv1.File_workos_project_v1_harness_binding_proto.Services().ByName("ProjectHarnessBindingService")
	if request == nil || request.Fields().ByName("credential_ref") != nil || request.Fields().ByName("resource_policy_id") != nil || request.Fields().ByName("instance_policy") != nil || request.Fields().ByName("profile_id") != nil {
		t.Fatalf("public binding request exposes private preset fields: %v", request)
	}
	if service == nil || service.Methods().Len() != 1 || service.Methods().ByName("SetProjectHarnessBinding") == nil {
		t.Fatalf("unexpected public binding service: %v", service)
	}
}
