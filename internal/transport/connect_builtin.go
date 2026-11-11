package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

type connectBuiltin struct {
	client pb.BuiltinExtensionServiceClient
}

func (s connectBuiltin) ListBuiltinSections(ctx context.Context, req *connect.Request[pb.ListBuiltinSectionsRequest]) (*connect.Response[pb.ListBuiltinSectionsResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListBuiltinSections)
}
func (s connectBuiltin) OpenBuiltinSection(ctx context.Context, req *connect.Request[pb.OpenBuiltinSectionRequest]) (*connect.Response[pb.OpenBuiltinSectionResponse], error) {
	return bridgeUnary(ctx, req, s.client.OpenBuiltinSection)
}
func (s connectBuiltin) ExecuteBuiltinCommand(ctx context.Context, req *connect.Request[pb.ExecuteBuiltinCommandRequest]) (*connect.Response[pb.ExecuteBuiltinCommandResponse], error) {
	return bridgeUnary(ctx, req, s.client.ExecuteBuiltinCommand)
}
