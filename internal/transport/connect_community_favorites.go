package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type connectCommunityFavorites struct {
	rpc.UnimplementedCommunityFavoriteServiceHandler
	client pb.CommunityFavoriteServiceClient
}

func (s connectCommunityFavorites) GetCommunityFavoriteStates(ctx context.Context, req *connect.Request[pb.GetCommunityFavoriteStatesRequest]) (*connect.Response[pb.GetCommunityFavoriteStatesResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetCommunityFavoriteStates)
}
func (s connectCommunityFavorites) SetCommunityFavorite(ctx context.Context, req *connect.Request[pb.SetCommunityFavoriteRequest]) (*connect.Response[pb.SetCommunityFavoriteResponse], error) {
	return bridgeUnary(ctx, req, s.client.SetCommunityFavorite)
}

func (s connectCommunityFavorites) ListCommunityFavorites(ctx context.Context, req *connect.Request[pb.ListCommunityFavoritesRequest]) (*connect.Response[pb.ListCommunityFavoritesResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListCommunityFavorites)
}
