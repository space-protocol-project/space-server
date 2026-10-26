package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type connectCommunityReactions struct {
	rpc.UnimplementedCommunityReactionServiceHandler
	client pb.CommunityReactionServiceClient
}

func (s connectCommunityReactions) ListCommunityReactions(ctx context.Context, req *connect.Request[pb.ListCommunityReactionsRequest]) (*connect.Response[pb.ListCommunityReactionsResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListCommunityReactions)
}
func (s connectCommunityReactions) SetCommunityReaction(ctx context.Context, req *connect.Request[pb.SetCommunityReactionRequest]) (*connect.Response[pb.SetCommunityReactionResponse], error) {
	return bridgeUnary(ctx, req, s.client.SetCommunityReaction)
}
