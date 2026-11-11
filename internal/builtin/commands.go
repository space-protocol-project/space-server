package builtin

import (
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

func NewService(registry *Registry, content pb.ContentServiceServer, mls pb.MlsServiceServer, forum pb.ForumServiceServer, feed pb.FeedServiceServer, rooms pb.VoiceServiceServer) *Service {
	s := &Service{registry: registry, commands: make(map[string]command)}
	bind(s, "space.chat", "/space.v1.ContentService/CreateContent", &pb.CreateContentRequest{}, content.CreateContent)
	bind(s, "space.chat", "/space.v1.ContentService/UpdateContent", &pb.UpdateContentRequest{}, content.UpdateContent)
	bind(s, "space.chat", "/space.v1.ContentService/DeleteContent", &pb.DeleteContentRequest{}, content.DeleteContent)
	bind(s, "space.chat", "/space.v1.ContentService/ApplyContentMutation", &pb.ApplyContentMutationRequest{}, content.ApplyContentMutation)
	bind(s, "space.chat", "/space.v1.ContentService/SetReaction", &pb.SetReactionRequest{}, content.SetReaction)
	bind(s, "space.chat", "/space.v1.MlsService/PublishMls", &pb.PublishMlsRequest{}, mls.PublishMls)
	bind(s, "space.rooms", "/space.v1.VoiceService/JoinVoiceRoom", &pb.JoinVoiceRoomRequest{}, rooms.JoinVoiceRoom)
	bind(s, "space.rooms", "/space.v1.VoiceService/UpdateVoiceRoom", &pb.UpdateVoiceRoomRequest{}, rooms.UpdateVoiceRoom)
	bind(s, "space.rooms", "/space.v1.VoiceService/LeaveVoiceRoom", &pb.LeaveVoiceRoomRequest{}, rooms.LeaveVoiceRoom)
	bind(s, "space.discussions", "/space.v1.ForumService/ReportForumContent", &pb.ReportForumContentRequest{}, forum.ReportForumContent)
	bind(s, "space.discussions", "/space.v1.ForumService/ResolveForumReport", &pb.ResolveForumReportRequest{}, forum.ResolveForumReport)
	bind(s, "space.discussions", "/space.v1.ForumService/CreateForumThread", &pb.CreateForumThreadRequest{}, forum.CreateForumThread)
	bind(s, "space.discussions", "/space.v1.ForumService/ReplyForumThread", &pb.ReplyForumThreadRequest{}, forum.ReplyForumThread)
	bind(s, "space.discussions", "/space.v1.ForumService/SetForumThreadPinned", &pb.SetForumThreadPinnedRequest{}, forum.SetForumThreadPinned)
	bind(s, "space.discussions", "/space.v1.ForumService/MarkForumThreadRead", &pb.MarkForumThreadReadRequest{}, forum.MarkForumThreadRead)
	bind(s, "space.discussions", "/space.v1.ForumService/CloseForumThread", &pb.CloseForumThreadRequest{}, forum.CloseForumThread)
	bind(s, "space.discussions", "/space.v1.ForumService/ModerateForumThread", &pb.ModerateForumThreadRequest{}, forum.ModerateForumThread)
	bind(s, "space.discussions", "/space.v1.ForumService/ModerateForumReply", &pb.ModerateForumReplyRequest{}, forum.ModerateForumReply)
	bind(s, "space.feed", "/space.v1.FeedService/CreateFeedPost", &pb.CreateFeedPostRequest{}, feed.CreateFeedPost)
	bind(s, "space.feed", "/space.v1.FeedService/CommentFeedPost", &pb.CommentFeedPostRequest{}, feed.CommentFeedPost)
	bind(s, "space.feed", "/space.v1.FeedService/UpdateFeedAccess", &pb.UpdateFeedAccessRequest{}, feed.UpdateFeedAccess)
	bind(s, "space.feed", "/space.v1.FeedService/ModerateFeedPost", &pb.ModerateFeedPostRequest{}, feed.ModerateFeedPost)
	bind(s, "space.feed", "/space.v1.FeedService/ModerateFeedComment", &pb.ModerateFeedCommentRequest{}, feed.ModerateFeedComment)
	bind(s, "space.feed", "/space.v1.FeedService/ReportFeedContent", &pb.ReportFeedContentRequest{}, feed.ReportFeedContent)
	bind(s, "space.feed", "/space.v1.FeedService/ResolveFeedReport", &pb.ResolveFeedReportRequest{}, feed.ResolveFeedReport)
	bind(s, "space.feed", "/space.v1.FeedService/MarkFeedPostRead", &pb.MarkFeedPostReadRequest{}, feed.MarkFeedPostRead)
	return s
}
