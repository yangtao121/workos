package systemhandler

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
)

type Handler struct {
	service      string
	version      string
	state        commonv1.HealthState
	capabilities []*commonv1.FeatureCapability
}

func New(service string, state commonv1.HealthState, capabilities ...*commonv1.FeatureCapability) *Handler {
	return &Handler{service: service, version: "0.1.0", state: state, capabilities: capabilities}
}

func (h *Handler) GetServiceHealth(context.Context, *connect.Request[commonv1.GetServiceHealthRequest]) (*connect.Response[commonv1.GetServiceHealthResponse], error) {
	health := &commonv1.ServiceHealth{
		Service: h.service, Version: h.version, State: h.state,
		Capabilities: h.capabilities, ObservedAt: timestamppb.New(time.Now().UTC()),
	}
	return connect.NewResponse(&commonv1.GetServiceHealthResponse{Health: health}), nil
}
