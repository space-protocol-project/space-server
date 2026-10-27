package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

type connectDirectoryPairing struct {
	client pb.DirectoryPairingServiceClient
}

func (s connectDirectoryPairing) CreateDirectoryTransfer(ctx context.Context, req *connect.Request[pb.CreateDirectoryTransferRequest]) (*connect.Response[pb.CreateDirectoryTransferResponse], error) {
	return bridgeUnary(ctx, req, s.client.CreateDirectoryTransfer)
}
func (s connectDirectoryPairing) OpenDirectoryTransfer(ctx context.Context, req *connect.Request[pb.OpenDirectoryTransferRequest]) (*connect.Response[pb.OpenDirectoryTransferResponse], error) {
	return bridgeUnary(ctx, req, s.client.OpenDirectoryTransfer)
}
func (s connectDirectoryPairing) WriteDirectoryFrame(ctx context.Context, req *connect.Request[pb.WriteDirectoryFrameRequest]) (*connect.Response[pb.WriteDirectoryFrameResponse], error) {
	return bridgeUnary(ctx, req, s.client.WriteDirectoryFrame)
}
func (s connectDirectoryPairing) ReadDirectoryFrame(ctx context.Context, req *connect.Request[pb.ReadDirectoryFrameRequest]) (*connect.Response[pb.ReadDirectoryFrameResponse], error) {
	return bridgeUnary(ctx, req, s.client.ReadDirectoryFrame)
}
func (s connectDirectoryPairing) CloseDirectoryTransfer(ctx context.Context, req *connect.Request[pb.CloseDirectoryTransferRequest]) (*connect.Response[pb.CloseDirectoryTransferResponse], error) {
	return bridgeUnary(ctx, req, s.client.CloseDirectoryTransfer)
}
