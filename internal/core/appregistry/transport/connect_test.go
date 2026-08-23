package transport

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/internal/core/appregistry/application"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// stubValidator and stubRepository let transport tests focus on identity,
// request bounds, projection shape, and error mapping.
type stubValidator struct{}

func (stubValidator) Validate(yamlBytes []byte) (domain.Manifest, []string) {
	if strings.Contains(string(yamlBytes), "invalid") {
		return domain.Manifest{}, []string{"/scope: scope 'system' requires a trusted installation path and cannot be self-registered"}
	}
	canonical := []byte(`{"apiVersion":"workos.app/v1","id":"notes","name":"Notes","scope":"user","version":"1.0.0"}`)
	return domain.Manifest{
		ID: "notes", Name: "Notes", Version: "1.0.0", Scope: domain.ScopeUser,
		Permissions: []string{"artifact.read"}, CanonicalJSON: canonical, Digest: domain.ManifestDigest(canonical),
	}, nil
}

type stubRepository struct{ stored []domain.AppVersion }

func (r *stubRepository) Register(_ context.Context, record domain.AppVersion) (domain.AppVersion, error) {
	for _, stored := range r.stored {
		if stored.OwnerUserID == record.OwnerUserID && stored.AppID == record.AppID && stored.Version == record.Version {
			if stored.ManifestDigest != record.ManifestDigest {
				return domain.AppVersion{}, domain.ErrVersionExists
			}
			return stored, nil
		}
	}
	r.stored = append(r.stored, record)
	return record, nil
}

func (r *stubRepository) GetVersion(_ context.Context, ownerUserID, appID, version string) (domain.AppVersion, error) {
	for _, stored := range r.stored {
		if stored.OwnerUserID == ownerUserID && stored.AppID == appID && stored.Version == version {
			return stored, nil
		}
	}
	return domain.AppVersion{}, domain.ErrNotFound
}

func (r *stubRepository) GetAppVersions(_ context.Context, ownerUserID, appID string) ([]domain.AppVersion, error) {
	var result []domain.AppVersion
	for _, stored := range r.stored {
		if stored.OwnerUserID == ownerUserID && stored.AppID == appID {
			result = append(result, stored)
		}
	}
	return result, nil
}

func (r *stubRepository) ListAppIDs(_ context.Context, ownerUserID, cursor string, limit int) ([]string, error) {
	seen := map[string]bool{}
	var result []string
	for _, stored := range r.stored {
		if stored.OwnerUserID == ownerUserID && stored.AppID > cursor && !seen[stored.AppID] {
			seen[stored.AppID] = true
			result = append(result, stored.AppID)
		}
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (r *stubRepository) GetVersionsForApps(_ context.Context, ownerUserID string, appIDs []string) ([]domain.AppVersion, error) {
	var result []domain.AppVersion
	for _, stored := range r.stored {
		if stored.OwnerUserID != ownerUserID {
			continue
		}
		for _, appID := range appIDs {
			if stored.AppID == appID {
				result = append(result, stored)
			}
		}
	}
	return result, nil
}

type stubProjects struct{}

func (stubProjects) Get(context.Context, string, string) (application.ProjectSummary, error) {
	return application.ProjectSummary{}, nil
}

type staticGenerator struct{}

func (staticGenerator) New() string { return "01999999-9999-7999-8999-999999999999" }

func newHandler(t *testing.T) *Handler {
	t.Helper()
	service, err := application.New(&stubRepository{}, stubValidator{}, stubProjects{}, staticGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	return New(service)
}

func withIdentity(ctx context.Context) context.Context {
	return identity.WithContext(ctx, identity.Identity{UserID: "owner-1", DeviceID: "device-1"})
}

func TestUnauthenticatedWithoutIdentity(t *testing.T) {
	t.Parallel()
	handler := newHandler(t)
	_, err := handler.GetApp(context.Background(), connect.NewRequest(&appv1.GetAppRequest{AppId: "notes"}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("missing identity must be Unauthenticated, got %v", err)
	}
	_, err = handler.RegisterApp(context.Background(), connect.NewRequest(&appv1.RegisterAppRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("missing identity must be Unauthenticated, got %v", err)
	}
	_, err = handler.ListApps(context.Background(), connect.NewRequest(&appv1.ListAppsRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("missing identity must be Unauthenticated, got %v", err)
	}
	_, err = handler.ValidateManifest(context.Background(), connect.NewRequest(&appv1.ValidateManifestRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("missing identity must be Unauthenticated, got %v", err)
	}
}

func TestValidateManifestReturnsViolationsNotErrors(t *testing.T) {
	t.Parallel()
	handler := newHandler(t)
	response, err := handler.ValidateManifest(withIdentity(context.Background()), connect.NewRequest(&appv1.ValidateManifestRequest{
		Yaml: []byte("invalid: true"),
	}))
	if err != nil {
		t.Fatalf("ordinary invalid input must be a valid=false response, not a Connect error: %v", err)
	}
	if response.Msg.GetValid() || len(response.Msg.GetViolations()) != 1 {
		t.Fatalf("unexpected response: %#v", response.Msg)
	}
	if response.Msg.GetNormalized() != nil {
		t.Fatal("invalid manifests must not carry a normalized projection")
	}

	valid, err := handler.ValidateManifest(withIdentity(context.Background()), connect.NewRequest(&appv1.ValidateManifestRequest{
		Yaml: []byte("apiVersion: workos.app/v1"),
	}))
	if err != nil || !valid.Msg.GetValid() {
		t.Fatalf("valid manifest rejected: %v %#v", err, valid.Msg)
	}
	normalized := valid.Msg.GetNormalized()
	if normalized.GetId() != "notes" || normalized.GetScope() != appv1.AppScope_APP_SCOPE_USER ||
		normalized.GetManifestDigest() == "" || normalized.GetPermissions()[0] != "artifact.read" {
		t.Fatalf("unexpected normalized projection: %#v", normalized)
	}

	_, err = handler.ValidateManifest(withIdentity(context.Background()), connect.NewRequest(&appv1.ValidateManifestRequest{
		Yaml: make([]byte, domain.MaxManifestBytes+1),
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversize manifest must be InvalidArgument, got %v", err)
	}
}

func TestRegisterProjectionAndErrors(t *testing.T) {
	t.Parallel()
	handler := newHandler(t)
	registered, err := handler.RegisterApp(withIdentity(context.Background()), connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: "key-1", ManifestYaml: []byte("apiVersion: workos.app/v1"),
	}))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	app := registered.Msg.GetApp()
	if app.GetId() != "notes" || app.GetVersion() != "1.0.0" || app.GetScope() != appv1.AppScope_APP_SCOPE_USER ||
		!strings.HasPrefix(app.GetManifestDigest(), "sha256:") {
		t.Fatalf("unexpected public projection: %#v", app)
	}

	_, err = handler.RegisterApp(withIdentity(context.Background()), connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: "key-2", ManifestYaml: []byte("invalid: true"),
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid manifest must be InvalidArgument, got %v", err)
	}
	if strings.Contains(err.Error(), "trusted installation path") {
		t.Fatalf("register must not echo validator violations: %v", err)
	}
	if _, err := handler.RegisterApp(withIdentity(context.Background()), connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: "key-3", ManifestYaml: make([]byte, domain.MaxManifestBytes+1),
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversize manifest must be InvalidArgument, got %v", err)
	}
}

func TestGetAndListShape(t *testing.T) {
	t.Parallel()
	handler := newHandler(t)
	if _, err := handler.RegisterApp(withIdentity(context.Background()), connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: "key-a", ManifestYaml: []byte("apiVersion: workos.app/v1"),
	})); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := handler.RegisterApp(withIdentity(context.Background()), connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: "key-b", ManifestYaml: []byte("apiVersion: workos.app/v1"),
	})); err != nil {
		t.Fatalf("same version and digest under a new key must replay: %v", err)
	}
	found, err := handler.GetApp(withIdentity(context.Background()), connect.NewRequest(&appv1.GetAppRequest{AppId: "notes"}))
	if err != nil || found.Msg.GetApp().GetVersion() != "1.0.0" {
		t.Fatalf("get current: %#v err=%v", found.Msg, err)
	}
	explicit, err := handler.GetApp(withIdentity(context.Background()), connect.NewRequest(&appv1.GetAppRequest{
		AppId: "notes", Version: "1.0.0",
	}))
	if err != nil || explicit.Msg.GetApp().GetVersion() != "1.0.0" {
		t.Fatalf("get explicit version: %#v err=%v", explicit.Msg, err)
	}
	missing, err := handler.GetApp(withIdentity(context.Background()), connect.NewRequest(&appv1.GetAppRequest{AppId: "ghost"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown app must be NotFound, got %v", err)
	}
	_ = missing

	listed, err := handler.ListApps(withIdentity(context.Background()), connect.NewRequest(&appv1.ListAppsRequest{
		Page: &commonv1.PageRequest{PageSize: 1},
	}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed.Msg.GetApps()) != 1 || listed.Msg.GetPage().GetNextPageToken() != "notes" {
		t.Fatalf("paging projection failed: %#v", listed.Msg)
	}
}

func TestMapErrorSanitizesInternalDetails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		code connect.Code
	}{
		{domain.ErrInvalid, connect.CodeInvalidArgument},
		{domain.ErrNotFound, connect.CodeNotFound},
		{domain.ErrVersionExists, connect.CodeAlreadyExists},
		{domain.ErrIdempotencyConflict, connect.CodeAborted},
		{errors.New("pq: duplicate key violates constraint app_versions_pkey at /var/lib/postgresql"), connect.CodeInternal},
	}
	for _, testCase := range cases {
		mapped := mapError(testCase.err)
		if connect.CodeOf(mapped) != testCase.code {
			t.Fatalf("expected %v for %v, got %v", testCase.code, testCase.err, mapped)
		}
		if strings.Contains(mapped.Error(), "app_versions_pkey") || strings.Contains(mapped.Error(), "/var/lib") {
			t.Fatalf("internal details leaked: %v", mapped)
		}
	}
}
