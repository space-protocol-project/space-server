package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type connectFeed struct {
	rpc.UnimplementedFeedServiceHandler
	client pb.FeedServiceClient
}

func (s connectFeed) ListFeedPosts(ctx context.Context, req *connect.Request[pb.ListFeedPostsRequest]) (*connect.Response[pb.ListFeedPostsResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListFeedPosts)
}
func (s connectFeed) GetFeedPost(ctx context.Context, req *connect.Request[pb.GetFeedPostRequest]) (*connect.Response[pb.GetFeedPostResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetFeedPost)
}
func (s connectFeed) CreateFeedPost(ctx context.Context, req *connect.Request[pb.CreateFeedPostRequest]) (*connect.Response[pb.CreateFeedPostResponse], error) {
	return bridgeUnary(ctx, req, s.client.CreateFeedPost)
}
func (s connectFeed) CommentFeedPost(ctx context.Context, req *connect.Request[pb.CommentFeedPostRequest]) (*connect.Response[pb.CommentFeedPostResponse], error) {
	return bridgeUnary(ctx, req, s.client.CommentFeedPost)
}
func (s connectFeed) GetFeedAccess(ctx context.Context, req *connect.Request[pb.GetFeedAccessRequest]) (*connect.Response[pb.GetFeedAccessResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetFeedAccess)
}
func (s connectFeed) UpdateFeedAccess(ctx context.Context, req *connect.Request[pb.UpdateFeedAccessRequest]) (*connect.Response[pb.UpdateFeedAccessResponse], error) {
	return bridgeUnary(ctx, req, s.client.UpdateFeedAccess)
}
func (s connectFeed) ModerateFeedPost(ctx context.Context, req *connect.Request[pb.ModerateFeedPostRequest]) (*connect.Response[pb.ModerateFeedPostResponse], error) {
	return bridgeUnary(ctx, req, s.client.ModerateFeedPost)
}
func (s connectFeed) ModerateFeedComment(ctx context.Context, req *connect.Request[pb.ModerateFeedCommentRequest]) (*connect.Response[pb.ModerateFeedCommentResponse], error) {
	return bridgeUnary(ctx, req, s.client.ModerateFeedComment)
}
func (s connectFeed) ListFeedModeration(ctx context.Context, req *connect.Request[pb.ListFeedModerationRequest]) (*connect.Response[pb.ListFeedModerationResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListFeedModeration)
}
func (s connectFeed) ReportFeedContent(ctx context.Context, req *connect.Request[pb.ReportFeedContentRequest]) (*connect.Response[pb.ReportFeedContentResponse], error) {
	return bridgeUnary(ctx, req, s.client.ReportFeedContent)
}
func (s connectFeed) ListFeedReports(ctx context.Context, req *connect.Request[pb.ListFeedReportsRequest]) (*connect.Response[pb.ListFeedReportsResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListFeedReports)
}
func (s connectFeed) ResolveFeedReport(ctx context.Context, req *connect.Request[pb.ResolveFeedReportRequest]) (*connect.Response[pb.ResolveFeedReportResponse], error) {
	return bridgeUnary(ctx, req, s.client.ResolveFeedReport)
}
