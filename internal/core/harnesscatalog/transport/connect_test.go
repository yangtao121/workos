package transport

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/yangtao121/workos/internal/platform/identity"

	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/core/harnesscatalog/application"
	"github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
)

type transportSourceFake struct{ err error }

func (f transportSourceFake) ListProviders(context.Context) ([]domain.Provider, error) {
	return nil, f.err
}

func TestCatalogTransportUsesSafeCanonicalErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		err  error
		code connect.Code
	}{
		{context.Canceled, connect.CodeCanceled},
		{context.DeadlineExceeded, connect.CodeDeadlineExceeded},
		{errors.New("dial private-host Authorization: secret"), connect.CodeUnavailable},
	} {
		service, err := application.New(transportSourceFake{err: test.err}, "fake")
		if err != nil {
			t.Fatal(err)
		}
		ctx := identity.WithContext(context.Background(), identity.Identity{UserID: "owner-1", DeviceID: "device-1"})
		_, got := New(service).GetHarnessCatalog(ctx, connect.NewRequest(&harnessv1.GetHarnessCatalogRequest{}))
		if connect.CodeOf(got) != test.code || got == nil || containsUnsafe(got.Error()) {
			t.Fatalf("unsafe catalog transport error: code=%s err=%v", connect.CodeOf(got), got)
		}
	}
}

func TestPublicCatalogServiceHasNoExecutionMethods(t *testing.T) {
	t.Parallel()
	service := harnessv1.File_workos_harness_v1_catalog_proto.Services().ByName("HarnessCatalogService")
	if service == nil || service.Methods().Len() != 1 || service.Methods().ByName("GetHarnessCatalog") == nil || service.Methods().ByName("ExecuteTask") != nil || service.Methods().ByName("CancelRun") != nil {
		t.Fatalf("unexpected public Catalog methods: %v", service)
	}
}

func containsUnsafe(value string) bool {
	for _, fragment := range []string{"private-host", "Authorization", "secret"} {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}
