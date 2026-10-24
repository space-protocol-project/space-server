package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type connectCommunitySearch struct {
	rpc.UnimplementedCommunitySearchServiceHandler
	client pb.CommunitySearchServiceClient
}

func (s connectCommunitySearch) SearchCommunityContent(ctx context.Context, req *connect.Request[pb.SearchCommunityContentRequest]) (*connect.Response[pb.SearchCommunityContentResponse], error) {
	return bridgeUnary(ctx, req, s.client.SearchCommunityContent)
}
