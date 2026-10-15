package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type connectMls struct {
	rpc.UnimplementedMlsServiceHandler
	client pb.MlsServiceClient
}

func (s connectMls) RegisterMlsDevice(ctx context.Context, req *connect.Request[pb.RegisterMlsDeviceRequest]) (*connect.Response[pb.RegisterMlsDeviceResponse], error) {
	return bridgeUnary(ctx, req, s.client.RegisterMlsDevice)
}
func (s connectMls) ListMlsDevices(ctx context.Context, req *connect.Request[pb.ListMlsDevicesRequest]) (*connect.Response[pb.ListMlsDevicesResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListMlsDevices)
}
func (s connectMls) GetMlsChannel(ctx context.Context, req *connect.Request[pb.GetMlsChannelRequest]) (*connect.Response[pb.GetMlsChannelResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetMlsChannel)
}
func (s connectMls) PublishMls(ctx context.Context, req *connect.Request[pb.PublishMlsRequest]) (*connect.Response[pb.PublishMlsResponse], error) {
	return bridgeUnary(ctx, req, s.client.PublishMls)
}
func (s connectMls) ListMlsEnvelopes(ctx context.Context, req *connect.Request[pb.ListMlsEnvelopesRequest]) (*connect.Response[pb.ListMlsEnvelopesResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListMlsEnvelopes)
}
