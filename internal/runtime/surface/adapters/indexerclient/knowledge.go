// The scoped indexer client adapter: the runtime's only door to the index
// projection. Every call derives owner/project exclusively from the validated
// surface session and forwards that trusted binding as this process's own
// identity headers — an app can never widen, override, or select the scope
// (ADR-0013 §E1). Responses are bounds-checked here before they can reach
// the bridge projection.
package indexerclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"connectrpc.com/connect"

	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	indexv1connect "github.com/yangtao121/workos/gen/go/workos/index/v1/indexv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// Response bounds mirror the indexer contract; anything outside them is
// malformed and fails closed before crossing into the bridge layer.
var (
	uuidV7Pattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reviewTypes     = map[string]bool{"document.markdown.v1": true, "code.unified-diff.v1": true}
	maxExcerptRunes = 200
	maxTitleRunes   = 200
)

type KnowledgeSearch struct {
	client   indexv1connect.IndexServiceClient
	deviceID string
	timeout  time.Duration
}

func NewKnowledgeSearch(indexerURL, deviceID string) (*KnowledgeSearch, error) {
	if indexerURL == "" {
		return nil, errors.New("knowledge search client requires the indexer URL")
	}
	return &KnowledgeSearch{
		client:   indexv1connect.NewIndexServiceClient(&http.Client{Timeout: 15 * time.Second}, indexerURL),
		deviceID: deviceID,
		timeout:  10 * time.Second,
	}, nil
}

func (k *KnowledgeSearch) Search(ctx context.Context, query ports.KnowledgeSearchQuery) (ports.KnowledgeSearchPage, error) {
	callCtx, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	request := connect.NewRequest(&indexv1.SearchRequest{
		ProjectId: query.ProjectID,
		Query:     query.Query,
		Page:      &commonv1.PageRequest{PageSize: query.PageSize, PageToken: query.PageToken},
	})
	// The trusted binding is the session-derived owner under this process's
	// device identity; it is overwritten, never merged.
	request.Header().Set(identity.UserHeader, query.OwnerUserID)
	request.Header().Set(identity.DeviceHeader, k.deviceID)
	response, err := k.client.Search(callCtx, request)
	if err != nil {
		return ports.KnowledgeSearchPage{}, mapIndexerError(err)
	}
	page := ports.KnowledgeSearchPage{
		NextPageToken: response.Msg.GetPage().GetNextPageToken(),
		CaughtUp:      response.Msg.GetFreshness().GetCaughtUp(),
	}
	for _, hit := range response.Msg.GetHits() {
		if !uuidV7Pattern.MatchString(hit.GetArtifactId()) || !digestPattern.MatchString(hit.GetDigest()) ||
			!reviewTypes[hit.GetArtifactType()] {
			return ports.KnowledgeSearchPage{}, ports.ErrKnowledgeMalformed
		}
		if len([]rune(hit.GetTitle())) == 0 || len([]rune(hit.GetTitle())) > maxTitleRunes ||
			len([]rune(hit.GetExcerpt())) > maxExcerptRunes {
			return ports.KnowledgeSearchPage{}, ports.ErrKnowledgeMalformed
		}
		score := hit.GetScore()
		if score != score || score < 0 || score > 3 {
			return ports.KnowledgeSearchPage{}, ports.ErrKnowledgeMalformed
		}
		ref := hit.GetSourceRef()
		if ref.GetType() != "artifact.review.v1" || ref.GetId() != hit.GetArtifactId() || ref.GetRevision() != hit.GetDigest() {
			return ports.KnowledgeSearchPage{}, ports.ErrKnowledgeMalformed
		}
		page.Hits = append(page.Hits, ports.KnowledgeHit{
			ArtifactID:   hit.GetArtifactId(),
			Digest:       hit.GetDigest(),
			ArtifactType: hit.GetArtifactType(),
			Title:        hit.GetTitle(),
			Excerpt:      hit.GetExcerpt(),
			Score:        score,
			CreatedAt:    hit.GetCreatedAt(),
		})
	}
	return page, nil
}

func mapIndexerError(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeCanceled:
		return fmt.Errorf("indexer is temporarily unavailable: %w", ports.ErrKnowledgeUnavailable)
	default:
		return fmt.Errorf("indexer call failed: %w", ports.ErrKnowledgeUnavailable)
	}
}
