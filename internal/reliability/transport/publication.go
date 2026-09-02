// Private transport for the reliability-host-owned incident notification
// publication source. It is registered on the reliability-host HTTP mux but
// never on the gateway allowlist, so browsers and apps deterministically
// cannot reach it; only the Core notification consumer reaches these RPCs
// over the internal network. Publications carry no content (ADR-0014).
package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	notificationv1 "github.com/yangtao121/workos/gen/go/workos/notification/v1"
	notificationv1connect "github.com/yangtao121/workos/gen/go/workos/notification/v1/notificationv1connect"
	"github.com/yangtao121/workos/internal/reliability/application"
	"github.com/yangtao121/workos/internal/reliability/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type publicationHandler struct {
	service *application.PublicationService
}

// NewPublicationSourceHandler wires the private source transport. The read
// limit is derived from the largest legal request: claim/complete batches
// of 16 tiny facts stay far below 16 KiB even with base64 inflation.
func NewPublicationSourceHandler(service *application.PublicationService) (string, http.Handler) {
	return notificationv1connect.NewIncidentNotificationPublicationSourceServiceHandler(
		&publicationHandler{service: service},
		connect.WithReadMaxBytes(16*1024),
	)
}

func (h *publicationHandler) ClaimIncidentPublications(ctx context.Context, req *connect.Request[notificationv1.ClaimIncidentPublicationsRequest]) (*connect.Response[notificationv1.ClaimIncidentPublicationsResponse], error) {
	claimed, err := h.service.Claim(ctx, application.ClaimInput{
		WorkerID:     req.Msg.GetWorkerId(),
		MaxBatch:     req.Msg.GetMaxBatch(),
		LeaseSeconds: req.Msg.GetLeaseSeconds(),
	})
	if err != nil {
		return nil, mapPublicationError(err)
	}
	publications := make([]*notificationv1.IncidentNotificationPublication, 0, len(claimed))
	for _, item := range claimed {
		publications = append(publications, &notificationv1.IncidentNotificationPublication{
			PublicationId: item.Publication.ID,
			IncidentId:    item.Publication.IncidentID,
			OwnerUserId:   item.Publication.OwnerUserID,
			ProjectId:     item.Publication.ProjectID,
			Severity:      item.Publication.Severity,
			ActionOutcome: item.Publication.ActionOutcome,
			OccurredAt:    timestamppb.New(item.Publication.OccurredAt),
			Digest:        item.Publication.Digest,
			LeaseToken:    item.LeaseToken,
		})
	}
	return connect.NewResponse(&notificationv1.ClaimIncidentPublicationsResponse{Publications: publications}), nil
}

func (h *publicationHandler) CompleteIncidentPublications(ctx context.Context, req *connect.Request[notificationv1.CompleteIncidentPublicationsRequest]) (*connect.Response[notificationv1.CompleteIncidentPublicationsResponse], error) {
	acked, err := h.service.Complete(ctx, application.CompleteInput{
		WorkerID:       req.Msg.GetWorkerId(),
		LeaseToken:     req.Msg.GetLeaseToken(),
		PublicationIDs: req.Msg.GetPublicationIds(),
	})
	if err != nil {
		return nil, mapPublicationError(err)
	}
	results := make([]*notificationv1.CompletedIncidentPublication, 0, len(acked))
	for _, result := range acked {
		results = append(results, &notificationv1.CompletedIncidentPublication{
			PublicationId: result.PublicationID,
			Acked:         result.Acked,
		})
	}
	return connect.NewResponse(&notificationv1.CompleteIncidentPublicationsResponse{Results: results}), nil
}

func mapPublicationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, application.ErrInvalidClaim), errors.Is(err, domain.ErrPublicationInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("publication claim is invalid"))
	case errors.Is(err, domain.ErrPublicationCorrupt):
		return connect.NewError(connect.CodeInternal, errors.New("publication operation failed"))
	}
	return connect.NewError(connect.CodeInternal, errors.New("publication operation failed"))
}
