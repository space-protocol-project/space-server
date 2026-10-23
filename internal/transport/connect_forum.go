package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type connectForum struct {
	rpc.UnimplementedForumServiceHandler
	client pb.ForumServiceClient
}

func (s connectForum) ListForumThreads(ctx context.Context, req *connect.Request[pb.ListForumThreadsRequest]) (*connect.Response[pb.ListForumThreadsResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListForumThreads)
}
func (s connectForum) GetForumThread(ctx context.Context, req *connect.Request[pb.GetForumThreadRequest]) (*connect.Response[pb.GetForumThreadResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetForumThread)
}
func (s connectForum) CreateForumThread(ctx context.Context, req *connect.Request[pb.CreateForumThreadRequest]) (*connect.Response[pb.CreateForumThreadResponse], error) {
	return bridgeUnary(ctx, req, s.client.CreateForumThread)
}
func (s connectForum) ReplyForumThread(ctx context.Context, req *connect.Request[pb.ReplyForumThreadRequest]) (*connect.Response[pb.ReplyForumThreadResponse], error) {
	return bridgeUnary(ctx, req, s.client.ReplyForumThread)
}
func (s connectForum) CloseForumThread(ctx context.Context, req *connect.Request[pb.CloseForumThreadRequest]) (*connect.Response[pb.CloseForumThreadResponse], error) {
	return bridgeUnary(ctx, req, s.client.CloseForumThread)
}

func (s connectForum) SetForumThreadPinned(ctx context.Context, req *connect.Request[pb.SetForumThreadPinnedRequest]) (*connect.Response[pb.SetForumThreadPinnedResponse], error) {
	return bridgeUnary(ctx, req, s.client.SetForumThreadPinned)
}

func (s connectForum) MarkForumThreadRead(ctx context.Context, req *connect.Request[pb.MarkForumThreadReadRequest]) (*connect.Response[pb.MarkForumThreadReadResponse], error) {
	return bridgeUnary(ctx, req, s.client.MarkForumThreadRead)
}

func (s connectForum) ModerateForumThread(ctx context.Context, req *connect.Request[pb.ModerateForumThreadRequest]) (*connect.Response[pb.ModerateForumThreadResponse], error) {
	return bridgeUnary(ctx, req, s.client.ModerateForumThread)
}

func (s connectForum) ModerateForumReply(ctx context.Context, req *connect.Request[pb.ModerateForumReplyRequest]) (*connect.Response[pb.ModerateForumReplyResponse], error) {
	return bridgeUnary(ctx, req, s.client.ModerateForumReply)
}

func (s connectForum) ListForumModeration(ctx context.Context, req *connect.Request[pb.ListForumModerationRequest]) (*connect.Response[pb.ListForumModerationResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListForumModeration)
}

func (s connectForum) ReportForumContent(ctx context.Context, req *connect.Request[pb.ReportForumContentRequest]) (*connect.Response[pb.ReportForumContentResponse], error) {
	return bridgeUnary(ctx, req, s.client.ReportForumContent)
}

func (s connectForum) ListForumReports(ctx context.Context, req *connect.Request[pb.ListForumReportsRequest]) (*connect.Response[pb.ListForumReportsResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListForumReports)
}

func (s connectForum) ResolveForumReport(ctx context.Context, req *connect.Request[pb.ResolveForumReportRequest]) (*connect.Response[pb.ResolveForumReportResponse], error) {
	return bridgeUnary(ctx, req, s.client.ResolveForumReport)
}
