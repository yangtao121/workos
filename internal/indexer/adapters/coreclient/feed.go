// Core feed client adapter: the indexer's only door to Core authority. It
// speaks the private IndexPublicationSourceService over the internal
// network, forwards this process's own identity (never a browser-supplied
// header), and maps Connect verdicts onto the port sentinels. Transient
// failures map to ErrCoreUnavailable; a stale claim is ordinary control
// flow, not a fault.
package coreclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"

	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	indexv1connect "github.com/yangtao121/workos/gen/go/workos/index/v1/indexv1connect"
	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type FeedClient struct {
	client      indexv1connect.IndexPublicationSourceServiceClient
	ownerUserID string
	deviceID    string
}

func NewFeedClient(coreURL, ownerUserID, deviceID string, timeout time.Duration) (*FeedClient, error) {
	if coreURL == "" {
		return nil, errors.New("core feed client requires the core URL")
	}
	httpClient := &http.Client{Timeout: timeout}
	return &FeedClient{
		client:      indexv1connect.NewIndexPublicationSourceServiceClient(httpClient, coreURL),
		ownerUserID: ownerUserID,
		deviceID:    deviceID,
	}, nil
}

// identityHeaders overwrite (never merge) the trusted internal identity on
// every call so a hostile environment variable cannot inject one.
func (f *FeedClient) identityHeaders(ctx context.Context, req connect.AnyRequest) {
	header := req.Header()
	header.Set(identity.UserHeader, f.ownerUserID)
	header.Set(identity.DeviceHeader, f.deviceID)
}

func (f *FeedClient) Claim(ctx context.Context, workerID string, maxBatch int, lease time.Duration) ([]ports.ClaimedPublication, error) {
	req := connect.NewRequest(&indexv1.ClaimIndexPublicationsRequest{
		WorkerId:     workerID,
		MaxBatch:     int32(maxBatch),
		LeaseSeconds: int32(lease / time.Second),
	})
	f.identityHeaders(ctx, req)
	response, err := f.client.ClaimIndexPublications(ctx, req)
	if err != nil {
		return nil, mapFeedError(err)
	}
	claimed := make([]ports.ClaimedPublication, 0, len(response.Msg.GetPublications()))
	for _, publication := range response.Msg.GetPublications() {
		claimed = append(claimed, ports.ClaimedPublication{
			PublicationID: publication.GetPublicationId(),
			Operation:     operationString(publication.GetOperation()),
			OwnerUserID:   publication.GetOwnerUserId(),
			ProjectID:     publication.GetProjectId(),
			SourceID:      publication.GetSourceId(),
			ArtifactType:  publication.GetArtifactType(),
			Digest:        publication.GetDigest(),
			OccurredAt:    publication.GetOccurredAt().AsTime(),
			LeaseToken:    publication.GetLeaseToken(),
		})
	}
	return claimed, nil
}

func (f *FeedClient) Resolve(ctx context.Context, workerID, publicationID, leaseToken string) (ports.ResolvedSource, error) {
	req := connect.NewRequest(&indexv1.ResolveIndexPublicationRequest{
		WorkerId: workerID, PublicationId: publicationID, LeaseToken: leaseToken,
	})
	f.identityHeaders(ctx, req)
	response, err := f.client.ResolveIndexPublication(ctx, req)
	if err != nil {
		return ports.ResolvedSource{}, mapFeedError(err)
	}
	msg := response.Msg
	resolved := ports.ResolvedSource{
		PublicationID: msg.GetPublication().GetPublicationId(),
		Operation:     operationString(msg.GetPublication().GetOperation()),
		OwnerUserID:   msg.GetPublication().GetOwnerUserId(),
		ProjectID:     msg.GetPublication().GetProjectId(),
	}
	switch msg.GetVerdict() {
	case indexv1.ResolveIndexPublicationResponse_VERDICT_RESOLVED:
		resolved.Verdict = "resolved"
		source := msg.GetSource()
		resolved.ArtifactID = source.GetArtifactId()
		resolved.SourceTaskID = source.GetSourceTaskId()
		resolved.ArtifactType = source.GetArtifactType()
		resolved.Digest = source.GetDigest()
		resolved.Title = source.GetTitle()
		resolved.Content = source.GetContent()
		resolved.CreatedAt = source.GetCreatedAt().AsTime()
		resolved.OccurredAt = msg.GetPublication().GetOccurredAt().AsTime()
	case indexv1.ResolveIndexPublicationResponse_VERDICT_TOMBSTONED:
		resolved.Verdict = "tombstoned"
	case indexv1.ResolveIndexPublicationResponse_VERDICT_CORRUPT:
		resolved.Verdict = "corrupt"
	default:
		return ports.ResolvedSource{}, domain.ErrCorrupt
	}
	return resolved, nil
}

func (f *FeedClient) Complete(ctx context.Context, workerID string, results []ports.ConsumptionResult) ([]bool, error) {
	wire := make([]*indexv1.IndexPublicationResult, 0, len(results))
	for _, result := range results {
		wire = append(wire, &indexv1.IndexPublicationResult{
			PublicationId: result.PublicationID,
			LeaseToken:    result.LeaseToken,
			Outcome:       outcomeProto(result.Outcome),
		})
	}
	req := connect.NewRequest(&indexv1.CompleteIndexPublicationsRequest{WorkerId: workerID, Results: wire})
	f.identityHeaders(ctx, req)
	response, err := f.client.CompleteIndexPublications(ctx, req)
	if err != nil {
		return nil, mapFeedError(err)
	}
	acked := make([]bool, 0, len(response.Msg.GetResults()))
	for _, result := range response.Msg.GetResults() {
		acked = append(acked, result.GetAcked())
	}
	return acked, nil
}

func (f *FeedClient) CountPending(ctx context.Context) (int64, error) {
	req := connect.NewRequest(&indexv1.CountPendingPublicationsRequest{})
	f.identityHeaders(ctx, req)
	response, err := f.client.CountPendingPublications(ctx, req)
	if err != nil {
		return 0, mapFeedError(err)
	}
	return response.Msg.GetPending(), nil
}

func (f *FeedClient) ReconcileSources(ctx context.Context, pageSize int, cursor string) ([]ports.ReconcileSource, string, string, error) {
	req := connect.NewRequest(&indexv1.ReconcileIndexSourcesRequest{PageSize: int32(pageSize), PageToken: cursor})
	f.identityHeaders(ctx, req)
	response, err := f.client.ReconcileIndexSources(ctx, req)
	if err != nil {
		return nil, "", "", mapFeedError(err)
	}
	sources := make([]ports.ReconcileSource, 0, len(response.Msg.GetSources()))
	for _, source := range response.Msg.GetSources() {
		sources = append(sources, ports.ReconcileSource{
			OwnerUserID:  source.GetOwnerUserId(),
			ProjectID:    source.GetProjectId(),
			ArtifactID:   source.GetArtifactId(),
			ArtifactType: source.GetArtifactType(),
			Digest:       source.GetDigest(),
			CreatedAt:    source.GetCreatedAt().AsTime(),
		})
	}
	return sources, response.Msg.GetNextPageToken(), response.Msg.GetSnapshotWatermark(), nil
}

func (f *FeedClient) ReconcileArchivedProjects(ctx context.Context, pageSize int, cursor string) ([]ports.ArchivedProject, string, error) {
	req := connect.NewRequest(&indexv1.ReconcileArchivedProjectsRequest{PageSize: int32(pageSize), PageToken: cursor})
	f.identityHeaders(ctx, req)
	response, err := f.client.ReconcileArchivedProjects(ctx, req)
	if err != nil {
		return nil, "", mapFeedError(err)
	}
	projects := make([]ports.ArchivedProject, 0, len(response.Msg.GetProjects()))
	for _, project := range response.Msg.GetProjects() {
		projects = append(projects, ports.ArchivedProject{
			OwnerUserID: project.GetOwnerUserId(),
			ProjectID:   project.GetProjectId(),
			ArchivedAt:  project.GetArchivedAt().AsTime(),
		})
	}
	return projects, response.Msg.GetNextPageToken(), nil
}

func (f *FeedClient) ResolveSourceContent(ctx context.Context, ownerUserID, projectID, artifactID, expectedDigest string) (ports.ResolvedSource, error) {
	req := connect.NewRequest(&indexv1.ResolveIndexSourceContentRequest{
		OwnerUserId: ownerUserID, ProjectId: projectID,
		ArtifactId: artifactID, ExpectedDigest: expectedDigest,
	})
	f.identityHeaders(ctx, req)
	response, err := f.client.ResolveIndexSourceContent(ctx, req)
	if err != nil {
		return ports.ResolvedSource{}, mapFeedError(err)
	}
	msg := response.Msg
	return ports.ResolvedSource{
		Verdict:      "resolved",
		OwnerUserID:  ownerUserID,
		ProjectID:    projectID,
		ArtifactID:   artifactID,
		ArtifactType: msg.GetArtifactType(),
		Digest:       msg.GetDigest(),
		Title:        msg.GetTitle(),
		Content:      msg.GetContent(),
		CreatedAt:    msg.GetCreatedAt().AsTime(),
	}, nil
}

func mapFeedError(err error) error {
	code := connect.CodeOf(err)
	switch code {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeCanceled, connect.CodeUnknown:
		return fmt.Errorf("%w: %v", ports.ErrCoreUnavailable, connect.CodeOf(err))
	case connect.CodeFailedPrecondition:
		return ports.ErrLeaseStale
	case connect.CodeNotFound:
		return ports.ErrNotFound
	case connect.CodeInvalidArgument:
		return domain.ErrInvalid
	default:
		return fmt.Errorf("index feed call failed: %d", code)
	}
}

func operationString(operation indexv1.IndexPublicationOperation) string {
	switch operation {
	case indexv1.IndexPublicationOperation_INDEX_PUBLICATION_OPERATION_REVIEW_ARTIFACT_UPSERT:
		return "review-artifact.upsert"
	case indexv1.IndexPublicationOperation_INDEX_PUBLICATION_OPERATION_PROJECT_TOMBSTONE:
		return "project.tombstone"
	default:
		return ""
	}
}

func outcomeProto(outcome string) indexv1.IndexPublicationOutcome {
	switch outcome {
	case "completed":
		return indexv1.IndexPublicationOutcome_INDEX_PUBLICATION_OUTCOME_COMPLETED
	case "tombstoned":
		return indexv1.IndexPublicationOutcome_INDEX_PUBLICATION_OUTCOME_TOMBSTONED
	case "unsupported":
		return indexv1.IndexPublicationOutcome_INDEX_PUBLICATION_OUTCOME_UNSUPPORTED
	case "corrupt":
		return indexv1.IndexPublicationOutcome_INDEX_PUBLICATION_OUTCOME_CORRUPT
	default:
		return indexv1.IndexPublicationOutcome_INDEX_PUBLICATION_OUTCOME_UNSPECIFIED
	}
}

var _ = timestamppb.New
