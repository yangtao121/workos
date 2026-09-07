package corepush

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	notificationv1 "github.com/yangtao121/workos/gen/go/workos/notification/v1"
	"github.com/yangtao121/workos/gen/go/workos/notification/v1/notificationv1connect"
	"github.com/yangtao121/workos/internal/gateway/auth/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Client struct {
	rpc notificationv1connect.DevicePushServiceClient
}

func New(client *http.Client, url string) *Client {
	return &Client{rpc: notificationv1connect.NewDevicePushServiceClient(client, url)}
}
func (c *Client) RevokeDevicePush(ctx context.Context, fact domain.PushRevocation) error {
	req := connect.NewRequest(&notificationv1.RevokeDevicePushRequest{RevokedAt: timestamppb.New(fact.RevokedAt), IdempotencyKey: fact.DeviceID})
	req.Header().Set(identity.UserHeader, fact.OwnerID)
	req.Header().Set(identity.DeviceHeader, fact.DeviceID)
	_, err := c.rpc.RevokeDevicePush(ctx, req)
	return err
}
