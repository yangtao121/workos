package indexerclient

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	"github.com/yangtao121/workos/gen/go/workos/index/v1/indexv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"math"
	"net/http/httptest"
	"strings"
	"testing"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

func validKnowledgeResponse() *indexv1.SearchHybridResponse {
	id := "01999999-9999-7999-8999-000000000071"
	digest := "sha256:" + strings.Repeat("d", 64)
	return &indexv1.SearchHybridResponse{
		Hits: []*indexv1.SearchHit{{
			ContextRef: "artifact.review.v1:" + id + ":" + digest,
			SourceRef:  &agentv1.ContextRef{Type: "artifact.review.v1", Id: id, Revision: digest},
			ArtifactId: id, ArtifactType: "document.markdown.v1", Digest: digest,
			Title: "Safe title", Excerpt: "safe excerpt", Score: 1,
			CreatedAt: "2026-09-01T00:00:00.000000Z",
		}},
		Page: &commonv1.PageResponse{},
		Freshness: &indexv1.IndexFreshness{CaughtUp: true,
			IndexedThrough: "2026-09-01T00:00:01.000000Z",
			LastIndexedAt:  "2026-09-01T00:00:01.000000Z"},
	}
}

func TestProjectKnowledgeResponseValidatesEveryBoundary(t *testing.T) {
	t.Parallel()
	if page, err := projectKnowledgeResponse(validKnowledgeResponse(), 20); err != nil || len(page.Hits) != 1 {
		t.Fatalf("valid response: page=%+v err=%v", page, err)
	}
	for name, mutate := range map[string]func(*indexv1.SearchHybridResponse){
		"raw ref drift":    func(response *indexv1.SearchHybridResponse) { response.Hits[0].ContextRef = "drift" },
		"title control":    func(response *indexv1.SearchHybridResponse) { response.Hits[0].Title = "bad\nname" },
		"excerpt control":  func(response *indexv1.SearchHybridResponse) { response.Hits[0].Excerpt = "bad\x00body" },
		"score above one":  func(response *indexv1.SearchHybridResponse) { response.Hits[0].Score = 1.01 },
		"workspace source": func(response *indexv1.SearchHybridResponse) { response.Hits[0].SourceRef.Type = "workspace.file.v1" },
		"score nan":        func(response *indexv1.SearchHybridResponse) { response.Hits[0].Score = math.NaN() },
		"created time":     func(response *indexv1.SearchHybridResponse) { response.Hits[0].CreatedAt = "not-time" },
		"freshness":        func(response *indexv1.SearchHybridResponse) { response.Freshness.PendingPublications = -1 },
		"token":            func(response *indexv1.SearchHybridResponse) { response.Page.NextPageToken = "not+base64url" },
		"too many hits":    func(response *indexv1.SearchHybridResponse) { response.Hits = append(response.Hits, response.Hits[0]) },
	} {
		response := validKnowledgeResponse()
		mutate(response)
		pageSize := 20
		if name == "too many hits" {
			pageSize = 1
		}
		if _, err := projectKnowledgeResponse(response, pageSize); err != ports.ErrKnowledgeMalformed {
			t.Fatalf("%s: error=%v", name, err)
		}
	}
}

type modelIndexHandler struct {
	indexv1connect.UnimplementedIndexServiceHandler
	requests chan *connect.Request[indexv1.SearchHybridRequest]
}

func (h *modelIndexHandler) SearchHybrid(_ context.Context, request *connect.Request[indexv1.SearchHybridRequest]) (*connect.Response[indexv1.SearchHybridResponse], error) {
	h.requests <- request
	return connect.NewResponse(validKnowledgeResponse()), nil
}

func TestKnowledgeUsesScopedModelRPC(t *testing.T) {
	handler := &modelIndexHandler{requests: make(chan *connect.Request[indexv1.SearchHybridRequest], 1)}
	_, route := indexv1connect.NewIndexServiceHandler(handler)
	server := httptest.NewServer(route)
	defer server.Close()
	device := "01999999-9999-7999-8999-000000000081"
	client, err := NewKnowledgeSearch(server.URL, device)
	if err != nil {
		t.Fatal(err)
	}
	query := ports.KnowledgeSearchQuery{OwnerUserID: "01999999-9999-7999-8999-000000000082", ProjectID: "01999999-9999-7999-8999-000000000083", Query: "中文查询", PageSize: 20, PageToken: "opaque-token"}
	page, err := client.Search(context.Background(), query)
	if err != nil || len(page.Hits) != 1 {
		t.Fatalf("model RPC failed: %+v %v", page, err)
	}
	request := <-handler.requests
	if request.Msg.ProjectId != query.ProjectID || request.Msg.SourceType != "artifact.review.v1" || request.Msg.Query != query.Query || request.Msg.Page.PageSize != query.PageSize || request.Msg.Page.PageToken != query.PageToken {
		t.Fatalf("model request changed its scope: %+v", request.Msg)
	}
	if request.Header().Get(identity.UserHeader) != query.OwnerUserID || request.Header().Get(identity.DeviceHeader) != device {
		t.Fatal("model request lost trusted identity")
	}
}

func TestKnowledgePreservesSanitizedIndexerVerdicts(t *testing.T) {
	for _, testCase := range []struct {
		code connect.Code
		want error
	}{
		{connect.CodeInvalidArgument, domain.ErrInvalid},
		{connect.CodeInternal, ports.ErrKnowledgeMalformed},
		{connect.CodeUnavailable, ports.ErrKnowledgeUnavailable},
		{connect.CodeDeadlineExceeded, ports.ErrKnowledgeUnavailable},
	} {
		got := mapIndexerError(connect.NewError(testCase.code, errors.New("private upstream detail")))
		if !errors.Is(got, testCase.want) || strings.Contains(got.Error(), "private upstream detail") {
			t.Fatalf("verdict %v: %v", testCase.code, got)
		}
	}
}
