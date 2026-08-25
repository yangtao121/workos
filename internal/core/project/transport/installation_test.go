package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	appv1connect "github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

const (
	ownerID      = "owner-1"
	projectID    = "01999999-9999-7999-8999-999999999992"
	installation = "01999999-9999-7999-8999-999999999993"
)

// stubInstallationRepository and stubCatalog keep transport tests focused on
// identity, projection shape, and error mapping.
type stubInstallationRepository struct {
	result ports.InstallationResult
	err    error
	items  []domain.Installation
	listFn func(owner, project, cursor string, limit int) ([]domain.Installation, error)
}

func (r *stubInstallationRepository) LookupInstallationRequest(context.Context, string, string) (ports.StoredInstallationRequest, bool, error) {
	return ports.StoredInstallationRequest{}, false, nil
}

func (r *stubInstallationRepository) GetInstallation(context.Context, string, string) (domain.Installation, error) {
	return domain.Installation{}, domain.ErrNotFound
}

func (r *stubInstallationRepository) Install(context.Context, ports.InstallCommand) (ports.InstallationResult, error) {
	return r.result, r.err
}

func (r *stubInstallationRepository) Uninstall(context.Context, ports.UninstallCommand) (ports.InstallationResult, error) {
	return r.result, r.err
}

func (r *stubInstallationRepository) ListActive(_ context.Context, ownerUserID, projectID, cursor string, limit int) ([]domain.Installation, error) {
	if r.listFn != nil {
		return r.listFn(ownerUserID, projectID, cursor, limit)
	}
	return r.items, nil
}

type stubCatalog struct{}

func (stubCatalog) Resolve(context.Context, string, string, string) (domain.PinnedApp, error) {
	return domain.PinnedApp{AppID: "board-app", Version: "1.2.0", ManifestDigest: "sha256:" + repeat("a", 64), Scope: "user"}, nil
}

type staticGenerator struct{}

func (staticGenerator) New() string { return "01999999-9999-7999-8999-999999999994" }

func newInstallationHandler(t *testing.T, repository ports.InstallationRepository) *InstallationHandler {
	t.Helper()
	service, err := application.NewInstallationService(repository, stubCatalog{}, staticGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	return NewInstallationHandler(service)
}

func withIdentity(ctx context.Context) context.Context {
	return identity.WithContext(ctx, identity.Identity{UserID: ownerID, DeviceID: "device-1"})
}

// newInstallationHTTPServer wires the real Connect handler behind an HTTP
// server with the identity middleware, exercising the actual wire boundary.
func newInstallationHTTPServer(t *testing.T, repository ports.InstallationRepository) *httptest.Server {
	t.Helper()
	service, err := application.NewInstallationService(repository, stubCatalog{}, staticGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	path, handler := NewInstallationConnectHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestInstallationUnauthenticatedWithoutIdentity(t *testing.T) {
	t.Parallel()
	handler := newInstallationHandler(t, &stubInstallationRepository{})
	ctx := context.Background()
	_, err := handler.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("install without identity must be Unauthenticated, got %v", err)
	}
	_, err = handler.UninstallApp(ctx, connect.NewRequest(&appv1.UninstallAppRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("uninstall without identity must be Unauthenticated, got %v", err)
	}
	_, err = handler.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("list without identity must be Unauthenticated, got %v", err)
	}
}

func TestInstallAppMapsDomainErrorsToSanitizedCodes(t *testing.T) {
	t.Parallel()
	installedAt := time.Now().UTC()
	cases := []struct {
		name     string
		err      error
		expected connect.Code
	}{
		{"invalid", domain.ErrInvalid, connect.CodeInvalidArgument},
		{"missing project", domain.ErrNotFound, connect.CodeNotFound},
		{"catalog denial", application.ErrAppNotInstallable, connect.CodeNotFound},
		{"different version", domain.ErrAlreadyInstalled, connect.CodeAlreadyExists},
		{"stale revision", domain.ErrConflict, connect.CodeAborted},
		{"idempotency conflict", domain.ErrIdempotencyConflict, connect.CodeAborted},
		{"internal", errors.New("sql: constraint app_versions_pkey violated"), connect.CodeInternal},
	}
	for _, testCase := range cases {
		handler := newInstallationHandler(t, &stubInstallationRepository{
			result: ports.InstallationResult{Installation: domain.Installation{InstalledAt: installedAt}, ProjectRevision: 2}, err: testCase.err,
		})
		response, err := handler.InstallApp(withIdentity(context.Background()), connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: "key", ProjectId: projectID, AppId: "board-app", ExpectedProjectRevision: 1,
		}))
		if connect.CodeOf(err) != testCase.expected {
			t.Fatalf("%s: expected %v, got %v (response %#v)", testCase.name, testCase.expected, err, response)
		}
		if err != nil && leaksInternals(err.Error()) {
			t.Errorf("%s: error leaked internals: %v", testCase.name, err)
		}
	}
}

func TestUninstallAppMapsDomainErrors(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		err      error
		expected connect.Code
	}{
		{domain.ErrNotFound, connect.CodeNotFound},
		{domain.ErrConflict, connect.CodeAborted},
		{errors.New("boom"), connect.CodeInternal},
	} {
		handler := newInstallationHandler(t, &stubInstallationRepository{err: testCase.err})
		_, err := handler.UninstallApp(withIdentity(context.Background()), connect.NewRequest(&appv1.UninstallAppRequest{
			IdempotencyKey: "key", ProjectId: projectID, InstallationId: installation, ExpectedProjectRevision: 3,
		}))
		if connect.CodeOf(err) != testCase.expected {
			t.Fatalf("expected %v, got %v", testCase.expected, err)
		}
	}
}

func TestInstallAppResponseProjection(t *testing.T) {
	t.Parallel()
	installedAt := time.Now().UTC()
	tombstone := installedAt.Add(time.Hour)
	handler := newInstallationHandler(t, &stubInstallationRepository{result: ports.InstallationResult{
		Installation: domain.Installation{
			ID: installation, OwnerUserID: ownerID, ProjectID: projectID, AppID: "board-app",
			Version: "1.2.0", ManifestDigest: "sha256:" + repeat("a", 64),
			InstalledAt: installedAt, UninstalledAt: &tombstone,
		},
		ProjectRevision: 7,
	}})
	response, err := handler.InstallApp(withIdentity(context.Background()), connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey: "key", ProjectId: projectID, AppId: "board-app", ExpectedProjectRevision: 6,
	}))
	if err != nil {
		t.Fatal(err)
	}
	proto := response.Msg.GetInstallation()
	if proto.GetId() != installation || proto.GetProjectId() != projectID || proto.GetAppId() != "board-app" ||
		proto.GetVersion() != "1.2.0" || proto.GetManifestDigest() != "sha256:"+repeat("a", 64) ||
		!proto.GetInstalledAt().AsTime().Equal(installedAt) || !proto.GetUninstalledAt().AsTime().Equal(tombstone) ||
		response.Msg.GetProjectRevision() != 7 {
		t.Fatalf("unexpected projection: %#v", proto)
	}
}

func TestListInstalledAppsForwardsPagingToken(t *testing.T) {
	t.Parallel()
	items := []domain.Installation{
		{ID: "i-1", ProjectID: projectID, AppID: "alpha-app", InstalledAt: time.Now().UTC()},
		{ID: "i-2", ProjectID: projectID, AppID: "beta-app", InstalledAt: time.Now().UTC()},
	}
	handler := newInstallationHandler(t, &stubInstallationRepository{items: items})
	response, err := handler.ListInstalledApps(withIdentity(context.Background()), connect.NewRequest(&appv1.ListInstalledAppsRequest{
		ProjectId: projectID, Page: &commonv1.PageRequest{PageSize: 1},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Msg.GetInstallations()) != 1 || response.Msg.GetInstallations()[0].GetAppId() != "alpha-app" {
		t.Fatalf("unexpected page: %#v", response.Msg.GetInstallations())
	}
	if response.Msg.GetPage().GetNextPageToken() != "alpha-app" {
		t.Fatalf("next token must be the page's last app id: %q", response.Msg.GetPage().GetNextPageToken())
	}
}

func TestInstallationHTTPWireUsesIdentityAndNormalizesPaging(t *testing.T) {
	t.Parallel()
	var seenLimit int
	var seenOwner string
	repository := &stubInstallationRepository{
		listFn: func(owner, _, _ string, limit int) ([]domain.Installation, error) {
			seenLimit, seenOwner = limit, owner
			return nil, nil
		},
	}
	server := newInstallationHTTPServer(t, repository)
	client := appv1connect.NewAppInstallationServiceClient(server.Client(), server.URL)
	request := connect.NewRequest(&appv1.ListInstalledAppsRequest{ProjectId: projectID})
	request.Header().Set(identity.UserHeader, ownerID)
	request.Header().Set(identity.DeviceHeader, "device-1")
	response, err := client.ListInstalledApps(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if seenOwner != ownerID {
		t.Fatalf("owner must come from the identity context, got %q", seenOwner)
	}
	if seenLimit != 51 {
		t.Fatalf("default page must probe 51 rows, got %d", seenLimit)
	}
	if response.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatal("empty result must not fabricate a token")
	}
}

func leaksInternals(message string) bool {
	for _, value := range []string{"sql:", "constraint", "pgx", "SELECT", "app_versions"} {
		if strings.Contains(message, value) {
			return true
		}
	}
	return false
}

func repeat(value string, count int) string {
	result := make([]byte, 0, count)
	for len(result) < count {
		result = append(result, value...)
	}
	return string(result[:count])
}
