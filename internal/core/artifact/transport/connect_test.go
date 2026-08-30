package transport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"connectrpc.com/connect"

	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	artifactv1connect "github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/internal/core/artifact/application"
	"github.com/yangtao121/workos/internal/core/artifact/domain"
	"github.com/yangtao121/workos/internal/core/artifact/ports"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// The transport tests run the real Connect handler with the real application
// service over a fake repository, so the read limits and error mapping are
// exercised exactly as production wires them.

func newHTTPServer(t *testing.T) (*httptest.Server, artifactv1connect.ArtifactServiceClient) {
	t.Helper()
	server, client, _ := newHTTPServerWithRepository(t)
	return server, client
}

func newHTTPServerWithRepository(t *testing.T) (*httptest.Server, artifactv1connect.ArtifactServiceClient, *transportRepository) {
	t.Helper()
	repository := newTransportRepository()
	service, err := application.New(repository, &transportGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	path, handler := NewConnectHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, artifactv1connect.NewArtifactServiceClient(server.Client(), server.URL), repository
}

const transportOwner = "0198d7ea-2110-7c42-b659-c5e4d73bc337"

// identityRequest stamps the trusted headers the gateway injects in
// production; without them the handler fails closed as Unauthenticated.
func identityRequest[V any](message *V) *connect.Request[V] {
	request := connect.NewRequest(message)
	request.Header().Set(identity.UserHeader, transportOwner)
	request.Header().Set(identity.DeviceHeader, transportOwner)
	return request
}

func TestCreateArtifactRoundTrip(t *testing.T) {
	t.Parallel()
	_, client := newHTTPServer(t)
	response, err := client.CreateArtifact(context.Background(), identityRequest(&artifactv1.CreateArtifactRequest{
		IdempotencyKey: "transport-1",
		Artifact:       &artifactv1.Artifact{Title: "Transport Bundle"},
		WebBundle: &artifactv1.WebBundleContent{
			Entrypoint: "index.html",
			Files: []*artifactv1.WebBundleFile{
				{Path: "index.html", Content: []byte("<!doctype html>")},
				{Path: "app.js", Content: []byte("console.log(1)")},
			},
		},
	}))
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	artifact := response.Msg.GetArtifact()
	if artifact.GetId() == "" || artifact.GetType() != domain.TypeWebBundle ||
		artifact.GetDigest() == "" || artifact.GetFileCount() != 2 || artifact.GetTotalSizeBytes() == 0 {
		t.Fatalf("unexpected artifact: %+v", artifact)
	}
	if artifact.GetCreatedAt() == nil || !strings.HasPrefix(artifact.GetContentRef(), "wbbnd:") {
		t.Fatalf("server-owned fields missing: %+v", artifact)
	}

	replay, err := client.CreateArtifact(context.Background(), identityRequest(&artifactv1.CreateArtifactRequest{
		IdempotencyKey: "transport-1",
		Artifact:       &artifactv1.Artifact{Title: "Transport Bundle"},
		WebBundle: &artifactv1.WebBundleContent{
			Entrypoint: "index.html",
			Files: []*artifactv1.WebBundleFile{
				{Path: "app.js", Content: []byte("console.log(1)")},
				{Path: "index.html", Content: []byte("<!doctype html>")},
			},
		},
	}))
	if err != nil || replay.Msg.GetArtifact().GetId() != artifact.GetId() {
		t.Fatalf("replay failed: %v", err)
	}
}

func TestArtifactConnectUsesSeparateCreateAndReadBudgets(t *testing.T) {
	t.Parallel()
	server, client, repository := newHTTPServerWithRepository(t)

	// A legal upload larger than the read-only budget must still reach the
	// Create handler's explicit 4 MiB budget.
	_, err := client.CreateArtifact(context.Background(), identityRequest(&artifactv1.CreateArtifactRequest{
		IdempotencyKey: "large-create-route",
		Artifact:       &artifactv1.Artifact{Title: "Large routed bundle"},
		WebBundle: &artifactv1.WebBundleContent{
			Entrypoint: "index.html",
			Files: []*artifactv1.WebBundleFile{{
				Path: "index.html", Content: []byte("<pre>" + strings.Repeat("x", MaxReadRequestBytes) + "</pre>"),
			}},
		},
	}))
	if err != nil {
		t.Fatalf("legal Create request above read budget was rejected: %v", err)
	}

	for name, options := range map[string][]connect.ClientOption{
		"protobuf":      nil,
		"protobuf gzip": {connect.WithSendGzip()},
		"json":          {connect.WithProtoJSON()},
		"json gzip":     {connect.WithProtoJSON(), connect.WithSendGzip()},
	} {
		t.Run(name, func(t *testing.T) {
			readClient := artifactv1connect.NewArtifactServiceClient(server.Client(), server.URL, options...)
			oversizedID := strings.Repeat("a", MaxReadRequestBytes+1024)
			_, err := readClient.GetArtifact(context.Background(), identityRequest(&artifactv1.GetArtifactRequest{
				ArtifactId: oversizedID,
			}))
			if connect.CodeOf(err) != connect.CodeResourceExhausted {
				t.Fatalf("oversized read must fail before decode, got %v", err)
			}
			if strings.Contains(err.Error(), oversizedID[:128]) {
				t.Fatalf("oversized request bytes leaked in error: %v", err)
			}
		})
	}
	if repository.getCalls != 0 {
		t.Fatalf("read business code ran for oversized requests: %d", repository.getCalls)
	}
}

func TestCreateArtifactRejectsServerOwnedMetadata(t *testing.T) {
	t.Parallel()
	_, client := newHTTPServer(t)
	for name, artifact := range map[string]*artifactv1.Artifact{
		"id":          {Title: "T", Id: "0198d7ea-2110-7c42-b659-c5e4d73bc337"},
		"project":     {Title: "T", ProjectId: "0198d7ea-2110-7c42-b659-c5e4d73bc337"},
		"type":        {Title: "T", Type: "custom.type"},
		"media":       {Title: "T", MediaType: "text/html"},
		"content_ref": {Title: "T", ContentRef: "/etc/passwd"},
		"digest":      {Title: "T", Digest: "sha256:" + strings.Repeat("a", 64)},
	} {
		_, err := client.CreateArtifact(context.Background(), identityRequest(&artifactv1.CreateArtifactRequest{
			IdempotencyKey: "md-" + name, Artifact: artifact,
			WebBundle: &artifactv1.WebBundleContent{
				Entrypoint: "index.html",
				Files:      []*artifactv1.WebBundleFile{{Path: "index.html", Content: []byte("x")}},
			},
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s metadata not rejected: %v", name, err)
		}
	}
}

func TestCreateArtifactWithoutBundleIsUnimplemented(t *testing.T) {
	t.Parallel()
	_, client := newHTTPServer(t)
	_, err := client.CreateArtifact(context.Background(), identityRequest(&artifactv1.CreateArtifactRequest{
		IdempotencyKey: "no-bundle", Artifact: &artifactv1.Artifact{Title: "T"},
	}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("unsupported payload verdict: %v", err)
	}
}

func TestArtifactErrorMapping(t *testing.T) {
	t.Parallel()
	_, client := newHTTPServer(t)
	_, err := client.GetArtifact(context.Background(), identityRequest(&artifactv1.GetArtifactRequest{
		ArtifactId: "0198d7ea-2110-7c42-b659-c5e4d73bc399",
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown artifact verdict: %v", err)
	}
	if got := err.Error(); strings.Contains(got, "sql") || strings.Contains(got, "pgx") {
		t.Fatalf("unsanitized error message: %s", got)
	}
	_, invalid := client.GetArtifact(context.Background(), identityRequest(&artifactv1.GetArtifactRequest{ArtifactId: "not-a-uuid"}))
	if connect.CodeOf(invalid) != connect.CodeInvalidArgument {
		t.Fatalf("malformed id verdict: %v", invalid)
	}
	// The boundary accepts only canonical lowercase UUIDv7: a well-formed
	// v4, a wrong variant, and an uppercase spelling are invalid arguments,
	// not unknown artifacts.
	for name, id := range map[string]string{
		"v4":         "9f8ee16a-4b46-4a8e-a6cc-82919bf8d0a8",
		"variant":    "0198d7ea-2110-7c42-c659-c5e4d73bc337",
		"uppercase":  "0198D7EA-2110-7C42-B659-C5E4D73BC337",
		"non-v7 hex": "0198d7ea-2110-4c42-b659-c5e4d73bc337",
	} {
		_, err := client.GetArtifact(context.Background(), identityRequest(&artifactv1.GetArtifactRequest{ArtifactId: id}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%s id verdict %v, want InvalidArgument", name, err)
		}
	}
	// A non-v7 cursor is an invalid argument before any storage read.
	_, cursor := client.ListArtifacts(context.Background(), identityRequest(&artifactv1.ListArtifactsRequest{
		Page: &commonv1.PageRequest{PageSize: 10, PageToken: "9f8ee16a-4b46-4a8e-a6cc-82919bf8d0a8"},
	}))
	if connect.CodeOf(cursor) != connect.CodeInvalidArgument {
		t.Fatalf("v4 cursor verdict %v, want InvalidArgument", cursor)
	}
	_, listed := client.ListArtifacts(context.Background(), identityRequest(&artifactv1.ListArtifactsRequest{
		ProjectId: "0198d7ea-2110-7c42-b659-c5e4d73bc337",
	}))
	if connect.CodeOf(listed) != connect.CodeUnimplemented {
		t.Fatalf("project-scoped listing verdict: %v", listed)
	}
}

func TestArtifactRequiresIdentity(t *testing.T) {
	t.Parallel()
	_, client := newHTTPServer(t)
	_, err := client.GetArtifact(context.Background(), connect.NewRequest(&artifactv1.GetArtifactRequest{
		ArtifactId: "0198d7ea-2110-7c42-b659-c5e4d73bc337",
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated verdict: %v", err)
	}
}

// transportRepository is a minimal in-memory fake with the consumed-key
// replay rule; deeper create/replay semantics are covered by the application
// tests.
type transportRepository struct {
	artifacts map[string]*artifactv1.Artifact // key: owner/idempotency
	next      int
	getCalls  int
}

func newTransportRepository() *transportRepository {
	return &transportRepository{artifacts: map[string]*artifactv1.Artifact{}}
}

func (r *transportRepository) Create(_ context.Context, command ports.CreateCommand) (domain.Artifact, error) {
	key := command.Artifact.OwnerUserID + "/" + command.IdempotencyKey
	if stored, ok := r.artifacts[key]; ok {
		return domain.Artifact{
			ID: stored.GetId(), OwnerUserID: command.Artifact.OwnerUserID,
			Type: stored.GetType(), Title: stored.GetTitle(), MediaType: stored.GetMediaType(),
			ContentRef: stored.GetContentRef(), Digest: stored.GetDigest(),
			FileCount: int(stored.GetFileCount()), TotalSizeBytes: stored.GetTotalSizeBytes(),
		}, nil
	}
	r.next++
	id := fmt.Sprintf("0198d7ea-2110-7c42-b659-%010dab", r.next)
	r.artifacts[key] = &artifactv1.Artifact{
		Id: id, Type: command.Artifact.Type, Title: command.Artifact.Title,
		MediaType: command.Artifact.MediaType, ContentRef: command.Artifact.ContentRef,
		Digest: command.Artifact.Digest, TotalSizeBytes: command.Artifact.TotalSizeBytes,
		FileCount: int32(command.Artifact.FileCount),
	}
	stored := command.Artifact
	stored.ID = id
	return stored, nil
}

func (r *transportRepository) Get(_ context.Context, ownerUserID, artifactID string) (domain.Artifact, error) {
	r.getCalls++
	return domain.Artifact{}, domain.ErrNotFound
}

func (r *transportRepository) ListIDsPage(_ context.Context, ownerUserID, cursor string, limit int) ([]string, string, error) {
	ids := make([]string, 0, len(r.artifacts))
	for _, artifact := range r.artifacts {
		ids = append(ids, artifact.GetId())
	}
	sortStrings(ids)
	result := []string{}
	for _, id := range ids {
		if id > cursor {
			result = append(result, id)
			if len(result) > limit {
				break
			}
		}
	}
	if len(result) <= limit {
		return result, "", nil
	}
	page := result[:limit]
	return page, page[len(page)-1], nil
}

func (r *transportRepository) VisitSummaries(_ context.Context, ownerUserID string, ids []string, visit func(domain.Artifact) error) error {
	for _, id := range ids {
		if err := visit(domain.Artifact{ID: id, Type: domain.TypeWebBundle}); err != nil {
			return err
		}
	}
	return nil
}

func (r *transportRepository) ReadAsset(_ context.Context, ownerUserID, artifactID, path string) (domain.BundleFile, error) {
	return domain.BundleFile{}, domain.ErrNotFound
}

type transportGenerator struct{ counter int }

func (g *transportGenerator) New() string {
	g.counter++
	return fmt.Sprintf("0198d7ea-2110-7c42-b659-%010dab", g.counter)
}

func sortStrings(values []string) {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
}

func TestListArtifactsPages(t *testing.T) {
	t.Parallel()
	_, client := newHTTPServer(t)
	for index := 0; index < 3; index++ {
		_, err := client.CreateArtifact(context.Background(), identityRequest(&artifactv1.CreateArtifactRequest{
			IdempotencyKey: fmt.Sprintf("page-%03d", index),
			Artifact:       &artifactv1.Artifact{Title: "T"},
			WebBundle: &artifactv1.WebBundleContent{
				Entrypoint: "index.html",
				Files:      []*artifactv1.WebBundleFile{{Path: "index.html", Content: []byte("x")}},
			},
		}))
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := client.ListArtifacts(context.Background(), identityRequest(&artifactv1.ListArtifactsRequest{
		Page: &commonv1.PageRequest{PageSize: 2},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Msg.GetArtifacts()) != 2 || page.Msg.GetPage().GetNextPageToken() == "" {
		t.Fatalf("unexpected first page: %d items token %q", len(page.Msg.GetArtifacts()), page.Msg.GetPage().GetNextPageToken())
	}
	next, err := client.ListArtifacts(context.Background(), identityRequest(&artifactv1.ListArtifactsRequest{
		Page: &commonv1.PageRequest{PageSize: 2, PageToken: page.Msg.GetPage().GetNextPageToken()},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Msg.GetArtifacts()) != 1 || next.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatalf("unexpected final page: %d items token %q", len(next.Msg.GetArtifacts()), next.Msg.GetPage().GetNextPageToken())
	}
}

func (r *transportRepository) GetReviewContent(_ context.Context, ownerUserID, artifactID string) (domain.ReviewArtifact, domain.NormalizedReviewContent, error) {
	return domain.ReviewArtifact{}, domain.NormalizedReviewContent{}, domain.ErrNotFound
}

func (r *transportRepository) ListProjectReviewIDsPage(_ context.Context, ownerUserID, projectID, cursor string, limit int) ([]string, string, error) {
	return nil, "", nil
}

func (r *transportRepository) FindTaskOutput(_ context.Context, _ dbtx.Tx, _, _ string) (ports.TaskOutputRecord, bool, error) {
	return ports.TaskOutputRecord{}, false, nil
}

func (r *transportRepository) InsertTaskOutput(_ context.Context, _ dbtx.Tx, _ ports.ReviewOutputCommand) (int64, error) {
	return 1, nil
}

func (r *transportRepository) ReviewArtifactByID(_ context.Context, _ dbtx.Tx, _ string) (domain.ReviewArtifact, error) {
	return domain.ReviewArtifact{}, domain.ErrNotFound
}
func (r *transportRepository) ReviewArtifactContentByID(context.Context, dbtx.Tx, string) (domain.ReviewArtifact, domain.NormalizedReviewContent, error) {
	return domain.ReviewArtifact{}, domain.NormalizedReviewContent{}, domain.ErrNotFound
}
