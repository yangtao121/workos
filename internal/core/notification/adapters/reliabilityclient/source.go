// Adapter connecting the Core notification consumer to the reliability-host
// private incident publication source over Connect. Only the internal
// network carries these calls; the gateway allowlist never routes this
// service. Owner/project identities inside the claim response are trusted
// because they come from the reliability-host's own durable facts, never
// from a browser.
package reliabilityclient

import (
	"context"

	"connectrpc.com/connect"

	notificationv1 "github.com/yangtao121/workos/gen/go/workos/notification/v1"
	notificationv1connect "github.com/yangtao121/workos/gen/go/workos/notification/v1/notificationv1connect"
	notificationapp "github.com/yangtao121/workos/internal/core/notification/application"
)

// Source implements application.IncidentPublicationSource.
type Source struct {
	client notificationv1connect.IncidentNotificationPublicationSourceServiceClient
}

func New(client notificationv1connect.IncidentNotificationPublicationSourceServiceClient) *Source {
	return &Source{client: client}
}

func (s *Source) ClaimIncidentPublications(ctx context.Context, workerID string, maxBatch int32, leaseSeconds int32) ([]notificationapp.IncidentPublication, error) {
	response, err := s.client.ClaimIncidentPublications(ctx, connect.NewRequest(&notificationv1.ClaimIncidentPublicationsRequest{
		WorkerId: workerID, MaxBatch: maxBatch, LeaseSeconds: leaseSeconds,
	}))
	if err != nil {
		return nil, err
	}
	publications := make([]notificationapp.IncidentPublication, 0, len(response.Msg.GetPublications()))
	for _, publication := range response.Msg.GetPublications() {
		publications = append(publications, notificationapp.IncidentPublication{
			PublicationID: publication.GetPublicationId(),
			IncidentID:    publication.GetIncidentId(),
			OwnerUserID:   publication.GetOwnerUserId(),
			ProjectID:     publication.GetProjectId(),
			Severity:      publication.GetSeverity(),
			ActionOutcome: publication.GetActionOutcome(),
			Digest:        publication.GetDigest(),
			OccurredAt:    publication.GetOccurredAt().AsTime(),
			LeaseToken:    publication.GetLeaseToken(),
		})
	}
	return publications, nil
}

func (s *Source) CompleteIncidentPublications(ctx context.Context, workerID, leaseToken string, publicationIDs []string) error {
	_, err := s.client.CompleteIncidentPublications(ctx, connect.NewRequest(&notificationv1.CompleteIncidentPublicationsRequest{
		WorkerId: workerID, LeaseToken: leaseToken, PublicationIds: publicationIDs,
	}))
	return err
}
