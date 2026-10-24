package postgres

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func TestCommunityOverviewPersonalReadStateAndModeration(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, true)
	peer, _ := f.enroll(t, true)
	section := forumSection(t, f, "overview_forum")
	fc := pb.NewForumServiceClient(f.conn)
	topic := createTopic(t, fc, member, section.Id, "mine")
	reply, err := fc.ReplyForumThread(peer, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Ответ вам", IdempotencyKey: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fc.ReportForumContent(peer, &pb.ReportForumContentRequest{ChannelId: section.Id, ThreadId: topic.Id, Reason: "Проверить"})
	if err != nil {
		t.Fatal(err)
	}
	api := pb.NewCommunityOverviewServiceClient(f.conn)
	req := &pb.GetCommunityOverviewRequest{ChannelId: section.Id}
	page, err := api.GetCommunityOverview(member, req)
	if err != nil || len(page.GetRecent()) != 1 || len(page.GetRepliesToYou()) != 1 || page.PendingReports != 0 || page.CanModerate {
		t.Fatal(page, err)
	}
	item := page.RepliesToYou[0]
	if item.Content.Id != topic.Id || item.UnreadRepliesToYou != 1 || item.FirstUnreadReplySequence != reply.Reply.Sequence || page.Recent[0].UnreadCount != 2 {
		t.Fatal(page)
	}
	page, err = api.GetCommunityOverview(peer, req)
	if err != nil || len(page.GetRepliesToYou()) != 0 {
		t.Fatal(page, err)
	}
	f.acl(t, section, principalRule(id, permissions(true, true, true, true)), roleRule("member", permissions(true, true, true, false)))
	page, err = api.GetCommunityOverview(member, req)
	if err != nil || page.PendingReports != 1 || !page.CanModerate {
		t.Fatal(page, err)
	}
	_, err = fc.MarkForumThreadRead(member, &pb.MarkForumThreadReadRequest{ChannelId: section.Id, ThreadId: topic.Id, ThroughSequence: reply.Reply.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	page, err = api.GetCommunityOverview(member, req)
	if err != nil || len(page.GetRepliesToYou()) != 0 || page.Recent[0].UnreadCount != 0 {
		t.Fatal(page, err)
	}
	_, err = fc.ModerateForumThread(f.owner, &pb.ModerateForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, ExpectedRevision: reply.Thread.Revision, Action: "hide", Reason: "Скрыть"})
	if err != nil {
		t.Fatal(err)
	}
	page, err = api.GetCommunityOverview(member, req)
	if err != nil || len(page.GetRecent()) != 0 || len(page.GetRepliesToYou()) != 0 {
		t.Fatal(page, err)
	}
	// Отсутствие scope управления скрывает и сам счётчик жалоб.
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET scopes=ARRAY['chat.read','chat.write'] WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	page, err = api.GetCommunityOverview(member, req)
	if err != nil || page.CanModerate || page.PendingReports != 0 {
		t.Fatal(page, err)
	}
	f.acl(t, section, principalRule(id, permissions(true, false, false, true)))
	_, err = api.GetCommunityOverview(member, req)
	requireCode(t, err, codes.PermissionDenied)
}

func TestCommunityOverviewFeedConnectAndAccessRevocation(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, true)
	section := feedSection(t, f, "overview_feed")
	first := feedCreate(t, f, f.owner, section.Id, "первая")
	second := feedCreate(t, f, f.owner, section.Id, "вторая")
	other := feedSection(t, f, "overview_other")
	feedCreate(t, f, f.owner, other.Id, "чужой раздел")
	server := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer server.Close()
	api := rpc.NewCommunityOverviewServiceClient(server.Client(), server.URL)
	md, _ := metadata.FromOutgoingContext(member)
	read := func() (*pb.GetCommunityOverviewResponse, error) {
		req := connect.NewRequest(&pb.GetCommunityOverviewRequest{ChannelId: section.Id, Limit: 1})
		req.Header().Set("Authorization", md.Get("authorization")[0])
		page, err := api.GetCommunityOverview(f.ctx, req)
		if err != nil {
			return nil, err
		}
		return page.Msg, nil
	}
	page, err := read()
	if err != nil || len(page.GetRecent()) != 1 || page.Recent[0].Content.Id != second.Id || page.Recent[0].ReadStateSupported || len(page.RepliesToYou) != 0 {
		t.Fatal(page, err)
	}
	_, err = pb.NewFeedServiceClient(f.conn).ModerateFeedPost(f.owner, &pb.ModerateFeedPostRequest{ChannelId: section.Id, PostId: second.Id, ExpectedRevision: second.Revision, Hidden: true, Reason: "Проверка"})
	if err != nil {
		t.Fatal(err)
	}
	page, err = read()
	if err != nil || page.Recent[0].Content.Id != first.Id {
		t.Fatal(page, err)
	}
	f.acl(t, section, roleRule("member", permissions(false, false, false, false)))
	_, err = read()
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
	f.acl(t, section, principalRule(id, permissions(true, true, false, false)))
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	_, err = read()
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal(err)
	}
	_, err = api.GetCommunityOverview(f.ctx, connect.NewRequest(&pb.GetCommunityOverviewRequest{ChannelId: section.Id}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal(err)
	}
	owner := pb.NewCommunityOverviewServiceClient(f.conn)
	_, err = owner.GetCommunityOverview(f.owner, &pb.GetCommunityOverviewRequest{ChannelId: section.Id, Limit: 21})
	requireCode(t, err, codes.InvalidArgument)
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE channels SET archived=true WHERE id=$1", section.Id); err != nil {
		t.Fatal(err)
	}
	_, err = owner.GetCommunityOverview(f.owner, &pb.GetCommunityOverviewRequest{ChannelId: section.Id})
	requireCode(t, err, codes.FailedPrecondition)
}

func TestCommunityOverviewHiddenReplyDoesNotCount(t *testing.T) {
	f := newChannelFixture(t)
	member, _ := f.enroll(t, true)
	section := forumSection(t, f, "overview_hidden")
	fc := pb.NewForumServiceClient(f.conn)
	topic := createTopic(t, fc, member, section.Id, "topic")
	reply, err := fc.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Скрытый ответ", IdempotencyKey: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fc.ModerateForumReply(f.owner, &pb.ModerateForumReplyRequest{ChannelId: section.Id, ThreadId: topic.Id, ReplyId: reply.Reply.Id, ExpectedRevision: reply.Reply.Revision, Hidden: true, Reason: "Причина"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := pb.NewCommunityOverviewServiceClient(f.conn).GetCommunityOverview(member, &pb.GetCommunityOverviewRequest{ChannelId: section.Id})
	if err != nil || len(page.GetRepliesToYou()) != 0 || page.Recent[0].UnreadCount != 1 {
		t.Fatal(page, err)
	}
}
