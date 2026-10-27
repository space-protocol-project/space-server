package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc"
)

type connectRemoteBackup struct{ client pb.RemoteBackupServiceClient }

func (s connectRemoteBackup) RevokeBackupSlot(ctx context.Context, req *connect.Request[pb.RevokeBackupSlotRequest]) (*connect.Response[pb.RevokeBackupSlotResponse], error) {
	return bridgeUnary(ctx, req, s.client.RevokeBackupSlot)
}
func (s connectRemoteBackup) CreateBackupSlot(ctx context.Context, req *connect.Request[pb.CreateBackupSlotRequest]) (*connect.Response[pb.CreateBackupSlotResponse], error) {
	return bridgeUnary(ctx, req, s.client.CreateBackupSlot)
}
func (s connectRemoteBackup) PutBackup(ctx context.Context, req *connect.Request[pb.PutBackupRequest]) (*connect.Response[pb.PutBackupResponse], error) {
	return bridgeUnary(ctx, req, s.client.PutBackup)
}
func (s connectRemoteBackup) ReadBackupHead(ctx context.Context, req *connect.Request[pb.ReadBackupHeadRequest]) (*connect.Response[pb.ReadBackupHeadResponse], error) {
	return bridgeUnary(ctx, req, s.client.ReadBackupHead)
}
func (s connectRemoteBackup) GetBackup(ctx context.Context, req *connect.Request[pb.GetBackupRequest]) (*connect.Response[pb.GetBackupResponse], error) {
	reply, err := s.client.GetBackup(bridgeContext(ctx, req.Header()), req.Msg, grpc.MaxCallRecvMsgSize(6*1024*1024))
	if err != nil {
		return nil, bridgeError(err)
	}
	return connect.NewResponse(reply), nil
}
