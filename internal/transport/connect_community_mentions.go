package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type connectCommunityMentions struct {
	rpc.UnimplementedCommunityMentionServiceHandler
	client pb.CommunityMentionServiceClient
}

func (s connectCommunityMentions) ListMentionCandidates(ctx context.Context, req *connect.Request[pb.ListMentionCandidatesRequest]) (*connect.Response[pb.ListMentionCandidatesResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListMentionCandidates)
}
func (s connectCommunityMentions) MarkCommunityMentionsRead(ctx context.Context, req *connect.Request[pb.MarkCommunityMentionsReadRequest]) (*connect.Response[pb.MarkCommunityMentionsReadResponse], error) {
	return bridgeUnary(ctx, req, s.client.MarkCommunityMentionsRead)
}
