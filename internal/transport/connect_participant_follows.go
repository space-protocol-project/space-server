package transport

import (
 "context"
 "connectrpc.com/connect"
 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type connectParticipantFollows struct {
 rpc.UnimplementedParticipantFollowServiceHandler
 client pb.ParticipantFollowServiceClient
}
func (s connectParticipantFollows) GetParticipantFollowStates(ctx context.Context,req *connect.Request[pb.GetParticipantFollowStatesRequest]) (*connect.Response[pb.GetParticipantFollowStatesResponse],error) {
 return bridgeUnary(ctx,req,s.client.GetParticipantFollowStates)
}
func (s connectParticipantFollows) SetParticipantFollow(ctx context.Context,req *connect.Request[pb.SetParticipantFollowRequest]) (*connect.Response[pb.SetParticipantFollowResponse],error) {
 return bridgeUnary(ctx,req,s.client.SetParticipantFollow)
}
func (s connectParticipantFollows) ListFollowingParticipants(ctx context.Context,req *connect.Request[pb.ListFollowingParticipantsRequest]) (*connect.Response[pb.ListFollowingParticipantsResponse],error) {
 return bridgeUnary(ctx,req,s.client.ListFollowingParticipants)
}
func (s connectParticipantFollows) ListPersonalFeed(ctx context.Context,req *connect.Request[pb.ListPersonalFeedRequest]) (*connect.Response[pb.ListPersonalFeedResponse],error) {
 return bridgeUnary(ctx,req,s.client.ListPersonalFeed)
}
