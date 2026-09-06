package transport

import (
	"connectrpc.com/connect"
	"context"
	bridgev1 "github.com/yangtao121/workos/gen/go/workos/bridge/v1"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

func (h *BridgeHandler) CreateReviewArtifact(ctx context.Context, req *connect.Request[bridgev1.CreateReviewArtifactRequest]) (*connect.Response[bridgev1.CreateReviewArtifactResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, mapBridgeError(domain.ErrUnauthenticated)
	}
	a, err := h.service.CreateReviewArtifact(ctx, id.UserID, id.DeviceID, req.Header().Get(identity.BridgeTokenHeader), ports.AppArtifactInput{Key: req.Msg.GetIdempotencyKey(), Type: req.Msg.GetType(), Title: req.Msg.GetTitle(), Content: req.Msg.GetContent()})
	if err != nil {
		return nil, mapBridgeError(err)
	}
	return connect.NewResponse(&bridgev1.CreateReviewArtifactResponse{Artifact: a}), nil
}
func (h *BridgeHandler) OpenReviewArtifact(ctx context.Context, req *connect.Request[bridgev1.OpenReviewArtifactRequest]) (*connect.Response[bridgev1.OpenReviewArtifactResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, mapBridgeError(domain.ErrUnauthenticated)
	}
	a, err := h.service.OpenReviewArtifact(ctx, id.UserID, id.DeviceID, req.Header().Get(identity.BridgeTokenHeader), req.Msg.GetArtifactId())
	if err != nil {
		return nil, mapBridgeError(err)
	}
	return connect.NewResponse(&bridgev1.OpenReviewArtifactResponse{Artifact: a}), nil
}
