package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type connectSpaceSearch struct {
	rpc.UnimplementedSpaceSearchServiceHandler
	client pb.SpaceSearchServiceClient
}

func (s connectSpaceSearch) SearchSpace(ctx context.Context, req *connect.Request[pb.SearchSpaceRequest]) (*connect.Response[pb.SearchSpaceResponse], error) {
	return bridgeUnary(ctx, req, s.client.SearchSpace)
}
