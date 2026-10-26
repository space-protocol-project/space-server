package transport

import (
 "context"
 "connectrpc.com/connect"
 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
 "google.golang.org/grpc"
)

type connectUserExport struct {
 rpc.UnimplementedUserExportServiceHandler
 client pb.UserExportServiceClient
}
func (s connectUserExport) ExportUserContent(ctx context.Context,req *connect.Request[pb.ExportUserContentRequest]) (*connect.Response[pb.ExportUserContentResponse],error) {
 return bridgeUnary(ctx,req,s.export)
}
func (s connectUserExport) export(ctx context.Context,req *pb.ExportUserContentRequest,options ...grpc.CallOption) (*pb.ExportUserContentResponse,error) {
 return s.client.ExportUserContent(ctx,req,append(options,grpc.MaxCallRecvMsgSize(16*1024*1024))...)
}
