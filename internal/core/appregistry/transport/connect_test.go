package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
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
	// Without an explicit id line the fixed default projection keeps the
	// legacy tests stable; manifests that declare identity use it.
	id, name, version := "notes", "Notes", "1.0.0"
	for _, line := range strings.Split(string(yamlBytes), "\n") {
		for prefix, target := range map[string]*string{"id: ": &id, "name: ": &name, "version: ": &version} {
			if strings.HasPrefix(line, prefix) {
				*target = strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
	}
	canonical := []byte(fmt.Sprintf(`{"id":%q,"name":%q,"version":%q}`, id, name, version))
	return domain.Manifest{
		ID: id, Name: name, Version: version, Scope: domain.ScopeUser,
		Permissions: []string{"artifact.read"}, CanonicalJSON: canonical, Digest: domain.ManifestDigest(canonical),
	}, nil
}

type stubRepository struct {
	stored        []domain.AppVersion
	registerCalls int
}

func (r *stubRepository) Register(_ context.Context, record domain.AppVersion) (domain.AppVersionSummary, error) {
	r.registerCalls++
	for _, stored := range r.stored {
		if stored.OwnerUserID == record.OwnerUserID && stored.AppID == record.AppID && stored.Version == record.Version {
			if stored.ManifestDigest != record.ManifestDigest {
				return domain.AppVersionSummary{}, domain.ErrVersionExists
			}
			return domain.SummaryOf(stored), nil
		}
	}
	r.stored = append(r.stored, record)
	return domain.SummaryOf(record), nil
}

func (r *stubRepository) GetVersionManifest(_ context.Context, ownerUserID, appID, version string) (string, []byte, error) {
	return "", nil, domain.ErrNotFound
}

func (r *stubRepository) GetVersion(_ context.Context, ownerUserID, appID, version string) (domain.AppVersionSummary, error) {
	for _, stored := range r.stored {
		if stored.OwnerUserID == ownerUserID && stored.AppID == appID && stored.Version == version {
			return domain.SummaryOf(stored), nil
		}
	}
	return domain.AppVersionSummary{}, domain.ErrNotFound
}

func (r *stubRepository) ListAppIDPage(_ context.Context, ownerUserID, cursor string, limit int) ([]string, string, error) {
	seen := map[string]bool{}
	var result []string
	for _, stored := range r.stored {
		if stored.OwnerUserID == ownerUserID && stored.AppID > cursor && !seen[stored.AppID] {
			seen[stored.AppID] = true
			result = append(result, stored.AppID)
		}
	}
	if len(result) <= limit {
		return result, "", nil
	}
	page := result[:limit]
	return page, page[len(page)-1], nil
}

func (r *stubRepository) VisitVersionSummaries(_ context.Context, ownerUserID string, appIDs []string, visit func(domain.AppVersionSummary) error) error {
	requested := make(map[string]bool, len(appIDs))
	for _, appID := range appIDs {
		requested[appID] = true
	}
	for _, stored := range r.stored {
		if stored.OwnerUserID == ownerUserID && requested[stored.AppID] {
			if err := visit(domain.SummaryOf(stored)); err != nil {
				return err
			}
		}
	}
	return nil
}

type stubProjects struct{}

func (stubProjects) Get(context.Context, string, string) (application.ProjectSummary, error) {
	return application.ProjectSummary{}, nil
}

type staticGenerator struct{}

func (staticGenerator) New() string { return "01999999-9999-7999-8999-999999999999" }

func newHandler(t *testing.T) *Handler {
	t.Helper()
	service, err := application.New(&stubRepository{}, stubValidator{}, stubProjects{}, nil, staticGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	return New(service)
}

func withIdentity(ctx context.Context) context.Context {
	return identity.WithContext(ctx, identity.Identity{UserID: "owner-1", DeviceID: "device-1"})
}

// newHTTPServer wires the real Connect handler (the same constructor the
// composition root uses) behind an HTTP server so tests exercise the actual
// wire boundary, including the pre-decode read limit.
func newHTTPServer(t *testing.T) (*httptest.Server, *stubRepository) {
	t.Helper()
	repository := &stubRepository{}
	service, err := application.New(repository, stubValidator{}, stubProjects{}, nil, staticGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	path, handler := NewConnectHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, repository
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
	// Idempotency keys follow the same grammar regardless of manifest size.
	for _, key := range []string{"", strings.Repeat("k", 129), "bad\x01key"} {
		_, err := handler.RegisterApp(withIdentity(context.Background()), connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: key, ManifestYaml: []byte("apiVersion: workos.app/v1"),
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("malformed idempotency key %q must be InvalidArgument, got %v", key, err)
		}
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
	if _, err := handler.GetApp(withIdentity(context.Background()), connect.NewRequest(&appv1.GetAppRequest{AppId: "ghost"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown app must be NotFound, got %v", err)
	}
	// Malformed app IDs are request errors, never database errors.
	if _, err := handler.GetApp(withIdentity(context.Background()), connect.NewRequest(&appv1.GetAppRequest{AppId: "Bad_ID"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("malformed app id must be InvalidArgument, got %v", err)
	}

	listed, err := handler.ListApps(withIdentity(context.Background()), connect.NewRequest(&appv1.ListAppsRequest{
		Page: &commonv1.PageRequest{PageSize: 1},
	}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// One stored app with page size one is exactly full: the application's
	// limit+1 probe finds no extra record, so no token is fabricated.
	if len(listed.Msg.GetApps()) != 1 || listed.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatalf("exactly-full page must not carry a token: %#v", listed.Msg)
	}
}

func TestListForwardsApplicationPageToken(t *testing.T) {
	t.Parallel()
	handler := newHandler(t)
	if _, err := handler.RegisterApp(withIdentity(context.Background()), connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: "key-a", ManifestYaml: []byte("apiVersion: workos.app/v1"),
	})); err != nil {
		t.Fatalf("register first: %v", err)
	}

	// A loose page holds every app without a token.
	first, err := handler.ListApps(withIdentity(context.Background()), connect.NewRequest(&appv1.ListAppsRequest{
		Page: &commonv1.PageRequest{PageSize: 10},
	}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(first.Msg.GetApps()) != 1 || first.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatalf("a non-full page must not carry a token: %#v", first.Msg.GetPage())
	}

	// With exactly one stored app, a page size of one is exactly full: the
	// application probe (limit+1) must NOT produce a token, and transport
	// must forward that absence verbatim instead of recomputing from the
	// raw page size.
	second, err := handler.ListApps(withIdentity(context.Background()), connect.NewRequest(&appv1.ListAppsRequest{
		Page: &commonv1.PageRequest{PageSize: 1},
	}))
	if err != nil {
		t.Fatalf("list exact page: %v", err)
	}
	if len(second.Msg.GetApps()) != 1 || second.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatalf("exactly-full page must have no token: %#v", second.Msg)
	}

	// Register a second app so the limit+1 probe has a real extra record and
	// the application emits a token that transport must forward untouched.
	if _, err := handler.RegisterApp(withIdentity(context.Background()), connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: "key-c", ManifestYaml: []byte("id: second\nname: Second\nversion: 2.0.0\nscope: user"),
	})); err != nil {
		t.Fatalf("register second: %v", err)
	}
	third, err := handler.ListApps(withIdentity(context.Background()), connect.NewRequest(&appv1.ListAppsRequest{
		Page: &commonv1.PageRequest{PageSize: 1},
	}))
	if err != nil {
		t.Fatalf("list paged: %v", err)
	}
	if len(third.Msg.GetApps()) != 1 || third.Msg.GetPage().GetNextPageToken() == "" {
		t.Fatalf("a real extra record must produce a forwarded token: %#v", third.Msg)
	}
	resumed, err := handler.ListApps(withIdentity(context.Background()), connect.NewRequest(&appv1.ListAppsRequest{
		Page: &commonv1.PageRequest{PageSize: 1, PageToken: third.Msg.GetPage().GetNextPageToken()},
	}))
	if err != nil {
		t.Fatalf("list resumed: %v", err)
	}
	if len(resumed.Msg.GetApps()) != 1 || resumed.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatalf("resumed page must be terminal: %#v", resumed.Msg)
	}

	// Malformed cursors are rejected as request errors.
	if _, err := handler.ListApps(withIdentity(context.Background()), connect.NewRequest(&appv1.ListAppsRequest{
		Page: &commonv1.PageRequest{PageSize: 1, PageToken: "not a cursor"},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("malformed cursor must be InvalidArgument, got %v", err)
	}
	// Negative page sizes are explicit request errors, and an absent page
	// block still means the default page size.
	if _, err := handler.ListApps(withIdentity(context.Background()), connect.NewRequest(&appv1.ListAppsRequest{
		Page: &commonv1.PageRequest{PageSize: -5},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("negative page size must be InvalidArgument, got %v", err)
	}
	bare, err := handler.ListApps(withIdentity(context.Background()), connect.NewRequest(&appv1.ListAppsRequest{}))
	if err != nil || len(bare.Msg.GetApps()) != 2 || bare.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatalf("absent page block must list every app: %#v err=%v", bare.Msg, err)
	}
}

func TestRegisterAppRejectsOversizedRequestBeforeDecode(t *testing.T) {
	t.Parallel()
	server, repository := newHTTPServer(t)
	client := appv1connect.NewAppRegistryServiceClient(http.DefaultClient, server.URL)

	oversized := make([]byte, MaxRequestBytes+256*1024)
	for i := range oversized {
		oversized[i] = 'x'
	}
	request := connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: "oversize", ManifestYaml: oversized})
	request.Header().Set(identity.UserHeader, "owner-1")
	request.Header().Set(identity.DeviceHeader, "device-1")
	_, err := client.RegisterApp(context.Background(), request)
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("oversized request must be ResourceExhausted before decoding, got %v", err)
	}
	if strings.Contains(err.Error(), "xxxxx") {
		t.Fatalf("oversize error must not echo the body: %v", err)
	}
	if repository.registerCalls != 0 {
		t.Fatalf("business handler must not execute for oversized bodies: %d calls", repository.registerCalls)
	}
}

func TestRegisterAppAcceptsManifestNearWireLimit(t *testing.T) {
	t.Parallel()
	server, repository := newHTTPServer(t)

	// A maximum-size legal manifest (256 KiB) must pass the wire budget in
	// every supported encoding. Binary protobuf costs ~5 framing bytes; the
	// protojson encoding inflates the bytes field 4/3 through base64 to
	// ~350 KiB, both under the 384 KiB read limit.
	manifest := bytes.Repeat([]byte{'#'}, domain.MaxManifestBytes)
	copy(manifest, []byte("id: notes\nname: Notes\nversion: 1.0.0\nscope: user\n#"))
	if len(manifest) != domain.MaxManifestBytes {
		t.Fatalf("fixture must be exactly the manifest limit: %d", len(manifest))
	}

	register := func(options ...connect.ClientOption) error {
		specialized := appv1connect.NewAppRegistryServiceClient(http.DefaultClient, server.URL, options...)
		request := connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: "wire-limit", ManifestYaml: manifest})
		request.Header().Set(identity.UserHeader, "owner-1")
		request.Header().Set(identity.DeviceHeader, "device-1")
		_, err := specialized.RegisterApp(context.Background(), request)
		return err
	}
	if err := register(); err != nil {
		t.Fatalf("binary protobuf at the business limit must pass: %v", err)
	}
	if err := register(connect.WithProtoJSON()); err != nil {
		t.Fatalf("connect JSON (base64-inflated) at the business limit must pass: %v", err)
	}
	if repository.registerCalls != 2 {
		t.Fatalf("both encodings must reach the business handler: %d", repository.registerCalls)
	}
}

func TestRegisterAppRejectsDecompressionBombs(t *testing.T) {
	t.Parallel()
	server, repository := newHTTPServer(t)
	client := appv1connect.NewAppRegistryServiceClient(http.DefaultClient, server.URL, connect.WithSendGzip())

	// A tiny compressed body whose decompressed message far exceeds the read
	// limit must be rejected on the decompressed size, not the wire size.
	bomb := bytes.Repeat([]byte{'x'}, MaxRequestBytes*4)
	request := connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: "bomb", ManifestYaml: bomb})
	request.Header().Set(identity.UserHeader, "owner-1")
	request.Header().Set(identity.DeviceHeader, "device-1")
	_, err := client.RegisterApp(context.Background(), request)
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("decompression bomb must be ResourceExhausted, got %v", err)
	}
	if repository.registerCalls != 0 {
		t.Fatalf("business handler must not execute for bombs: %d calls", repository.registerCalls)
	}
}

func TestValidateManifestRejectsOversizedWireRequests(t *testing.T) {
	t.Parallel()
	server, _ := newHTTPServer(t)
	client := appv1connect.NewAppRegistryServiceClient(http.DefaultClient, server.URL)
	request := connect.NewRequest(&appv1.ValidateManifestRequest{
		Yaml: bytes.Repeat([]byte{'x'}, MaxRequestBytes+128*1024),
	})
	request.Header().Set(identity.UserHeader, "owner-1")
	request.Header().Set(identity.DeviceHeader, "device-1")
	if _, err := client.ValidateManifest(context.Background(), request); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("oversized validate request must be ResourceExhausted, got %v", err)
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
