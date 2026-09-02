package indexerclient

import (
	"math"
	"strings"
	"testing"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

func validKnowledgeResponse() *indexv1.SearchResponse {
	id := "01999999-9999-7999-8999-000000000071"
	digest := "sha256:" + strings.Repeat("d", 64)
	return &indexv1.SearchResponse{
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
	for name, mutate := range map[string]func(*indexv1.SearchResponse){
		"raw ref drift":   func(response *indexv1.SearchResponse) { response.Hits[0].ContextRef = "drift" },
		"title control":   func(response *indexv1.SearchResponse) { response.Hits[0].Title = "bad\nname" },
		"excerpt control": func(response *indexv1.SearchResponse) { response.Hits[0].Excerpt = "bad\x00body" },
		"score nan":       func(response *indexv1.SearchResponse) { response.Hits[0].Score = math.NaN() },
		"created time":    func(response *indexv1.SearchResponse) { response.Hits[0].CreatedAt = "not-time" },
		"freshness":       func(response *indexv1.SearchResponse) { response.Freshness.PendingPublications = -1 },
		"token":           func(response *indexv1.SearchResponse) { response.Page.NextPageToken = "not+base64url" },
		"too many hits":   func(response *indexv1.SearchResponse) { response.Hits = append(response.Hits, response.Hits[0]) },
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
