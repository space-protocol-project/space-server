package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type connectPersonalNotifications struct {
	rpc.UnimplementedPersonalNotificationServiceHandler
	client pb.PersonalNotificationServiceClient
}

func (s connectPersonalNotifications) ListPersonalNotifications(ctx context.Context, req *connect.Request[pb.ListPersonalNotificationsRequest]) (*connect.Response[pb.ListPersonalNotificationsResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListPersonalNotifications)
}

func (s connectPersonalNotifications) MarkPersonalNotificationsRead(ctx context.Context, req *connect.Request[pb.MarkPersonalNotificationsReadRequest]) (*connect.Response[pb.MarkPersonalNotificationsReadResponse], error) {
	return bridgeUnary(ctx, req, s.client.MarkPersonalNotificationsRead)
}
