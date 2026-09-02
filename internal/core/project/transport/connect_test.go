package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	appv1connect "github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	projectv1connect "github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	"github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

const handlerOwner = "owner-1"

var validProjectUUID = "01999999-9999-7999-8999-999999999992"

// stubProjectRepository backs the ProjectService transport tests: it records
// which commands survived the wire boundary and answers with canned verdicts.
type stubProjectRepository struct {
	lookups     int
	creates     int
	getCalls    int
	listCalls   int
	updateCalls int
	archiveCall int

	lookupStored ports.StoredCreateRequest
	lookupFound  bool
	createErr    error
	getProject   domain.Project
	getErr       error
	listItems    []domain.Project
	listErr      error
	updateErr    error
	archiveErr   error
}

func (r *stubProjectRepository) LookupCreateRequest(context.Context, string, string) (ports.StoredCreateRequest, bool, error) {
	r.lookups++
	return r.lookupStored, r.lookupFound, nil
}

func (r *stubProjectRepository) CreateProject(context.Context, ports.CreateCommand) (domain.Project, error) {
	r.creates++
	if r.createErr != nil {
		return domain.Project{}, r.createErr
	}
	return domain.Project{ID: validProjectUUID, OwnerUserID: handlerOwner, Name: "Created", Revision: 1}, nil
}

func (r *stubProjectRepository) GetProject(context.Context, string, string) (domain.Project, error) {
	r.getCalls++
	if r.getErr != nil {
		return domain.Project{}, r.getErr
	}
	if r.getProject.ID == "" {
		return domain.Project{}, domain.ErrNotFound
	}
	return r.getProject, nil
}

func (r *stubProjectRepository) ListProjects(_ context.Context, _, _ string, limit int, _ bool) ([]domain.Project, error) {
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	if len(r.listItems) > limit {
		return r.listItems[:limit], nil
	}
	return r.listItems, nil
}

func (r *stubProjectRepository) UpdateProject(context.Context, domain.Project, int64) (domain.Project, error) {
	r.updateCalls++
	if r.updateErr != nil {
		return domain.Project{}, r.updateErr
	}
	return domain.Project{ID: validProjectUUID, Revision: 2}, nil
}

func (r *stubProjectRepository) ArchiveProject(context.Context, string, string, int64) (domain.Project, error) {
	r.archiveCall++
	if r.archiveErr != nil {
		return domain.Project{}, r.archiveErr
	}
	return domain.Project{ID: validProjectUUID, Revision: 2}, nil
}

type stubGenerator struct{}

func (stubGenerator) New() string { return "01999999-9999-7999-8999-999999999994" }

func newProjectHandler(t *testing.T, repository ports.Repository) *Handler {
	t.Helper()
	return New(application.New(repository, stubGenerator{}))
}

func withProjectIdentity(ctx context.Context) context.Context {
	return identity.WithContext(ctx, identity.Identity{UserID: handlerOwner, DeviceID: "device-1"})
}

// newProjectHTTPServer wires the real Connect handler with the bounded-read
// constructor behind an HTTP server and the identity middleware, exercising
// the actual wire boundary.
func newProjectHTTPServer(t *testing.T, repository ports.Repository) *httptest.Server {
	t.Helper()
	path, handler := NewProjectConnectHandler(application.New(repository, stubGenerator{}))
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestProjectServiceUnauthenticatedWithoutIdentity(t *testing.T) {
	t.Parallel()
	handler := newProjectHandler(t, &stubProjectRepository{})
	ctx := context.Background()
	requests := map[string]func() error{
		"create": func() error {
			_, err := handler.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{}))
			return err
		},
		"get": func() error {
			_, err := handler.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{}))
			return err
		},
		"list": func() error {
			_, err := handler.ListProjects(ctx, connect.NewRequest(&projectv1.ListProjectsRequest{}))
			return err
		},
		"update": func() error {
			_, err := handler.UpdateProject(ctx, connect.NewRequest(&projectv1.UpdateProjectRequest{}))
			return err
		},
		"archive": func() error {
			_, err := handler.ArchiveProject(ctx, connect.NewRequest(&projectv1.ArchiveProjectRequest{}))
			return err
		},
	}
	for name, call := range requests {
		err := call()
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("%s without identity must be Unauthenticated, got %v", name, err)
		}
		if err != nil && !strings.Contains(err.Error(), "authentication is required") {
			t.Errorf("%s must carry the fixed message, got %q", name, err.Error())
		}
	}
}

func TestProjectServiceMapsDomainErrorsToSanitizedCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		repo     *stubProjectRepository
		expected connect.Code
	}{
		{"invalid", &stubProjectRepository{createErr: domain.ErrInvalid}, connect.CodeInvalidArgument},
		{"missing", &stubProjectRepository{createErr: domain.ErrNotFound}, connect.CodeNotFound},
		{"conflict", &stubProjectRepository{createErr: domain.ErrConflict}, connect.CodeAborted},
		{"idempotency conflict", &stubProjectRepository{createErr: domain.ErrIdempotencyConflict}, connect.CodeAborted},
		{"store unavailable", &stubProjectRepository{createErr: fmt.Errorf("query project: %w: %w", ports.ErrStoreUnavailable, errors.New("driver died"))}, connect.CodeUnavailable},
		{"internal", &stubProjectRepository{createErr: errors.New("sql: constraint projects_pkey violated")}, connect.CodeInternal},
	}
	for _, testCase := range cases {
		handler := newProjectHandler(t, testCase.repo)
		_, err := handler.CreateProject(withProjectIdentity(context.Background()), connect.NewRequest(&projectv1.CreateProjectRequest{
			IdempotencyKey: "key", Name: "Name",
		}))
		if connect.CodeOf(err) != testCase.expected {
			t.Errorf("%s: expected %v, got %v", testCase.name, testCase.expected, err)
		}
		if err != nil && leaksProjectInternals(err.Error()) {
			t.Errorf("%s: error leaked internals: %v", testCase.name, err)
		}
	}
}

// leaksProjectInternals reports whether a wire error carries storage or
// driver details.
func leaksProjectInternals(message string) bool {
	for _, value := range []string{"sql:", "constraint", "pgx", "SELECT", "projects", "driver died"} {
		if strings.Contains(message, value) {
			return true
		}
	}
	return false
}

func TestCreateProjectHTTPDefaultPagingAndNextToken(t *testing.T) {
	t.Parallel()
	repository := &stubProjectRepository{}
	repository.listItems = []domain.Project{
		{ID: "01999999-9999-7999-8999-999999999991"},
		{ID: "01999999-9999-7999-8999-999999999992"},
		{ID: "01999999-9999-7999-8999-999999999993"},
	}
	server := newProjectHTTPServer(t, repository)
	client := projectv1connect.NewProjectServiceClient(server.Client(), server.URL)
	request := connect.NewRequest(&projectv1.ListProjectsRequest{Page: &commonv1.PageRequest{PageSize: 2}})
	request.Header().Set(identity.UserHeader, handlerOwner)
	request.Header().Set(identity.DeviceHeader, "device-1")
	response, err := client.ListProjects(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Msg.GetProjects()) != 2 {
		t.Fatalf("page must carry the effective page size: %d", len(response.Msg.GetProjects()))
	}
	if token := response.Msg.GetPage().GetNextPageToken(); token != "01999999-9999-7999-8999-999999999992" {
		t.Fatalf("next token must be the last returned project id: %q", token)
	}
	// Default (no page) also works and does not fabricate a token.
	empty := &stubProjectRepository{}
	emptyServer := newProjectHTTPServer(t, empty)
	emptyClient := projectv1connect.NewProjectServiceClient(emptyServer.Client(), emptyServer.URL)
	emptyRequest := connect.NewRequest(&projectv1.ListProjectsRequest{})
	emptyRequest.Header().Set(identity.UserHeader, handlerOwner)
	emptyRequest.Header().Set(identity.DeviceHeader, "device-1")
	emptyResponse, err := emptyClient.ListProjects(context.Background(), emptyRequest)
	if err != nil {
		t.Fatal(err)
	}
	if emptyResponse.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatal("empty page must not fabricate a token")
	}
	if empty.listCalls != 1 {
		t.Fatalf("default page must reach the repository once: %d", empty.listCalls)
	}
}

func TestProjectServiceRejectsOversizedBodiesBeforeDecode(t *testing.T) {
	t.Parallel()
	repository := &stubProjectRepository{}
	server := newProjectHTTPServer(t, repository)
	client := projectv1connect.NewProjectServiceClient(server.Client(), server.URL)

	// A legal near-max request passes the budget and reaches the business
	// layer: 16 refs of ~1 KiB URIs ≈ 16 KiB, comfortably inside the
	// derivation headroom while exercising the largest legal shape.
	legalRefs := make([]*projectv1.WorkspaceRef, 0, domain.MaxWorkspaceRefs)
	for index := 0; index < domain.MaxWorkspaceRefs; index++ {
		legalRefs = append(legalRefs, &projectv1.WorkspaceRef{
			Id: fmt.Sprintf("ref-%02d", index), Kind: projectv1.WorkspaceKind_WORKSPACE_KIND_NAS,
			Uri: "nfs:///" + strings.Repeat("p", domain.MaxWorkspaceRefURIRunes-8),
		})
	}
	legal := connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: "wire-legal", Name: "Legal Size", WorkspaceRefs: legalRefs,
	})
	legal.Header().Set(identity.UserHeader, handlerOwner)
	legal.Header().Set(identity.DeviceHeader, "device-1")
	if _, err := client.CreateProject(context.Background(), legal); err != nil {
		t.Fatalf("legal near-max request must pass the wire budget: %v", err)
	}
	if repository.creates != 1 {
		t.Fatalf("legal request must reach the business handler: %d creates", repository.creates)
	}

	// An oversized single URI is rejected before decode with the fixed
	// ResourceExhausted verdict and zero business executions.
	oversize := connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: "wire-oversize", Name: "Too Big",
		WorkspaceRefs: []*projectv1.WorkspaceRef{{
			Id: "ref", Kind: projectv1.WorkspaceKind_WORKSPACE_KIND_NAS,
			Uri: "nfs:///" + strings.Repeat("p", MaxProjectRequestBytes),
		}},
	})
	oversize.Header().Set(identity.UserHeader, handlerOwner)
	oversize.Header().Set(identity.DeviceHeader, "device-1")
	_, err := client.CreateProject(context.Background(), oversize)
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("oversized request must be ResourceExhausted before decode, got %v", err)
	}
	if strings.Contains(err.Error(), "nfs://") {
		t.Fatalf("oversize error must not echo the body: %v", err)
	}
	if repository.creates != 1 {
		t.Fatalf("business handler must not execute for oversized bodies: %d creates", repository.creates)
	}
}

func TestProjectServiceRejectsDecompressionBombs(t *testing.T) {
	t.Parallel()
	repository := &stubProjectRepository{}
	server := newProjectHTTPServer(t, repository)
	client := projectv1connect.NewProjectServiceClient(server.Client(), server.URL, connect.WithSendGzip())

	bomb := connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: "bomb", Name: "Bomb",
		WorkspaceRefs: []*projectv1.WorkspaceRef{{
			Id: "ref", Kind: projectv1.WorkspaceKind_WORKSPACE_KIND_NAS,
			Uri: "nfs:///" + strings.Repeat("p", MaxProjectRequestBytes*4),
		}},
	})
	bomb.Header().Set(identity.UserHeader, handlerOwner)
	bomb.Header().Set(identity.DeviceHeader, "device-1")
	if _, err := client.CreateProject(context.Background(), bomb); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("decompression bomb must be ResourceExhausted, got %v", err)
	}
	if repository.creates != 0 || repository.lookups != 0 {
		t.Fatalf("business code must not run for bombs: creates=%d lookups=%d", repository.creates, repository.lookups)
	}
}

func TestProjectServiceWireLimitIsIndependentPerHandler(t *testing.T) {
	t.Parallel()
	projectRepository := &stubProjectRepository{}
	installationRepository := &stubInstallationRepository{}
	projectServer := newProjectHTTPServer(t, projectRepository)
	installationServer := newInstallationHTTPServer(t, installationRepository)

	// One oversized capability list that exceeds the Project budget but fits
	// the installation budget: the installation handler accepts it while the
	// project handler rejects the same wire size.
	medium := make([]string, 0, 12000)
	for index := 0; index < 12000; index++ {
		medium = append(medium, "cap."+fmt.Sprintf("%06d", index)+".aaaa")
	}

	install := connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey: "own-limit", ProjectId: projectID, AppId: "board-app", ExpectedProjectRevision: 1,
		GrantedPermissions: medium,
	})
	install.Header().Set(identity.UserHeader, ownerID)
	install.Header().Set(identity.DeviceHeader, "device-1")
	// The installation handler must decode the same wire size (its budget
	// stays 288 KiB): a business-level verdict — not a pre-decode
	// ResourceExhausted — proves the body crossed its wire boundary. The
	// synthetic capability ids fail the pinned-manifest subset rule after
	// decode, which is exactly the point: decoding happened.
	installClient := appv1connect.NewAppInstallationServiceClient(installationServer.Client(), installationServer.URL)
	if _, err := installClient.InstallApp(context.Background(), install); err != nil {
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			t.Fatalf("installation handler must keep its own larger budget, got %v", err)
		}
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("installation body must be decoded (business verdict), got %v", err)
		}
	}

	create := connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: "own-limit", Name: "Project",
		WorkspaceRefs: []*projectv1.WorkspaceRef{{
			Id: "ref", Kind: projectv1.WorkspaceKind_WORKSPACE_KIND_NAS,
			Uri: "nfs:///" + strings.Repeat("p", MaxProjectRequestBytes),
		}},
	})
	create.Header().Set(identity.UserHeader, handlerOwner)
	create.Header().Set(identity.DeviceHeader, "device-1")
	projectClient := projectv1connect.NewProjectServiceClient(projectServer.Client(), projectServer.URL)
	if _, err := projectClient.CreateProject(context.Background(), create); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("project handler must keep its own tighter budget, got %v", err)
	}
	if projectRepository.creates != 0 {
		t.Fatal("project business code must not run for oversized bodies")
	}
}

func TestCreateProjectTransportValidationBeforeBusiness(t *testing.T) {
	t.Parallel()
	repository := &stubProjectRepository{}
	handler := newProjectHandler(t, repository)
	ctx := withProjectIdentity(context.Background())

	// Unknown workspace kind enum value and UNSPECIFIED never reach the
	// application with a valid-looking kind.
	for name, kind := range map[string]projectv1.WorkspaceKind{
		"unspecified": projectv1.WorkspaceKind_WORKSPACE_KIND_UNSPECIFIED,
		"unknown":     projectv1.WorkspaceKind(99),
	} {
		if _, err := handler.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
			IdempotencyKey: "kind", Name: "Name",
			WorkspaceRefs: []*projectv1.WorkspaceRef{{Id: "r", Kind: kind, Uri: "file:///x"}},
		})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s kind must be InvalidArgument, got %v", name, err)
		}
	}
	// Nil repeated item must not panic and must be rejected.
	if _, err := handler.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: "nil-item", Name: "Name",
		WorkspaceRefs: []*projectv1.WorkspaceRef{nil},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("nil workspace ref must be InvalidArgument, got %v", err)
	}
	if repository.lookups != 0 || repository.creates != 0 {
		t.Fatalf("invalid create requests must not touch the repository: %d/%d", repository.lookups, repository.creates)
	}
}

func TestArchiveProjectMapsMissingToNotFound(t *testing.T) {
	t.Parallel()
	handler := newProjectHandler(t, &stubProjectRepository{})
	_, err := handler.ArchiveProject(withProjectIdentity(context.Background()), connect.NewRequest(&projectv1.ArchiveProjectRequest{
		ProjectId: validProjectUUID, ExpectedRevision: 4,
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing project must be NotFound, got %v", err)
	}
}

func (r *stubProjectRepository) ReconcileArchivedProjectsPage(context.Context, string, int) ([]ports.ArchivedProjectRef, string, error) {
	return nil, "", errors.New("not used in this test")
}
