package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type connectCommunityOverview struct {
	rpc.UnimplementedCommunityOverviewServiceHandler
	client pb.CommunityOverviewServiceClient
}

func (s connectCommunityOverview) GetCommunityOverview(ctx context.Context, req *connect.Request[pb.GetCommunityOverviewRequest]) (*connect.Response[pb.GetCommunityOverviewResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetCommunityOverview)
}
