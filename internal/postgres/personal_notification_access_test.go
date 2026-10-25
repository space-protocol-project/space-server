package postgres

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPersonalNotificationsAccessRechecked(t *testing.T) {
	for _, scenario := range []struct {
		name, query string
		code        codes.Code
	}{
		{"scope", "UPDATE device_grants SET scopes=ARRAY['chat.write'] WHERE principal_id=$1", codes.PermissionDenied},
		{"revoked", "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", codes.Unauthenticated},
		{"blocked", "UPDATE memberships SET blocked=true WHERE principal_id=$1", codes.PermissionDenied},
		{"archived", "UPDATE channels SET archived=true WHERE id='general' AND $1<>''", codes.FailedPrecondition},
		{"mls", "UPDATE channels SET public_preview=false,mls_group_id=decode(repeat('01',32),'hex') WHERE id='general' AND $1<>''", codes.FailedPrecondition},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newChannelFixture(t)
			recipient, m := mentionRecipient(t, f, "Маша")
			mentionedChat(t, f, m, "access")
			page := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general"})
			if _, err := f.store.pool.Exec(f.ctx, scenario.query, m.PrincipalId); err != nil {
				t.Fatal(err)
			}
			api := pb.NewPersonalNotificationServiceClient(f.conn)
			_, err := api.ListPersonalNotifications(recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general"})
			requireCode(t, err, scenario.code)
			_, err = api.MarkPersonalNotificationsRead(recipient, &pb.MarkPersonalNotificationsReadRequest{ChannelId: "general", Ids: []string{page.Notifications[0].Id}})
			requireCode(t, err, scenario.code)
		})
	}
}

func TestPersonalNotificationsValidationACLAndConnect(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "Маша")
	c := f.create(t, "notice_private")
	created, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: c.Id, Text: "@Маша", IdempotencyKey: "private", Mentions: []*pb.CommunityMention{m}})
	if err != nil {
		t.Fatal(err)
	}
	api := pb.NewPersonalNotificationServiceClient(f.conn)
	for _, req := range []*pb.ListPersonalNotificationsRequest{
		{ChannelId: c.Id, AfterSequence: -1}, {ChannelId: c.Id, ThroughSequence: -1}, {ChannelId: c.Id, AfterSequence: 2},
		{ChannelId: c.Id, ThroughSequence: 2}, {ChannelId: c.Id, Limit: 51}, {ChannelId: c.Id, Limit: -1},
		{ChannelId: c.Id, BaselineOnly: true, AfterSequence: 1},
	} {
		_, err = api.ListPersonalNotifications(recipient, req)
		requireCode(t, err, codes.InvalidArgument)
	}
	_, err = api.ListPersonalNotifications(f.ctx, &pb.ListPersonalNotificationsRequest{ChannelId: c.Id})
	requireCode(t, err, codes.Unauthenticated)
	for _, ids := range [][]string{nil, {""}, {strings.Repeat("x", 129)}, make([]string, 51)} {
		_, err = api.MarkPersonalNotificationsRead(recipient, &pb.MarkPersonalNotificationsReadRequest{ChannelId: c.Id, Ids: ids})
		requireCode(t, err, codes.InvalidArgument)
	}
	web := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer web.Close()
	client := rpc.NewPersonalNotificationServiceClient(web.Client(), web.URL)
	req := connect.NewRequest(&pb.ListPersonalNotificationsRequest{ChannelId: c.Id})
	md, _ := metadata.FromOutgoingContext(recipient)
	req.Header().Set("Authorization", md.Get("authorization")[0])
	page, err := client.ListPersonalNotifications(f.ctx, req)
	if err != nil || len(page.Msg.Notifications) != 1 || page.Msg.Notifications[0].ContentId != created.Content.Id {
		t.Fatal(page, err)
	}
	mark := connect.NewRequest(&pb.MarkPersonalNotificationsReadRequest{ChannelId: c.Id, Ids: []string{page.Msg.Notifications[0].Id}})
	mark.Header().Set("Authorization", md.Get("authorization")[0])
	if _, err = client.MarkPersonalNotificationsRead(f.ctx, mark); err != nil {
		t.Fatal(err)
	}
	f.acl(t, c, principalRule(m.PrincipalId, permissions(false, false, false, false)))
	_, err = client.ListPersonalNotifications(f.ctx, req)
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
	_, err = client.MarkPersonalNotificationsRead(f.ctx, mark)
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
}

func TestPersonalNotificationsFeedReadAndIndependentMention(t *testing.T) {
	f := newChannelFixture(t)
	other, _ := f.enroll(t, true)
	section := feedSection(t, f, "notification_feed")
	feedPolicy(t, f, section, feedRole("member", false, true))
	api := pb.NewFeedServiceClient(f.conn)
	post, err := api.CreateFeedPost(f.owner, &pb.CreateFeedPostRequest{ChannelId: section.Id, Text: "Публикация", IdempotencyKey: "post"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE memberships SET nickname='Автор' WHERE principal_id=$1", f.ownerID); err != nil {
		t.Fatal(err)
	}
	comment, err := api.CommentFeedPost(other, &pb.CommentFeedPostRequest{ChannelId: section.Id, PostId: post.Post.Id, Text: "@Автор", IdempotencyKey: "comment", Mentions: []*pb.CommunityMention{{PrincipalId: f.ownerID, DisplayName: "Автор", Length: 6}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.MarkFeedPostRead(f.owner, &pb.MarkFeedPostReadRequest{ChannelId: section.Id, PostId: post.Post.Id, ThroughSequence: comment.Comment.Sequence}); err != nil {
		t.Fatal(err)
	}
	page := notificationPage(t, f, f.owner, &pb.ListPersonalNotificationsRequest{ChannelId: section.Id, UnreadOnly: true})
	if len(page.Notifications) != 1 || page.Notifications[0].Reasons != 3 || page.Notifications[0].UnreadReasons != 1 {
		t.Fatal(page)
	}
	_, err = pb.NewPersonalNotificationServiceClient(f.conn).MarkPersonalNotificationsRead(f.owner, &pb.MarkPersonalNotificationsReadRequest{ChannelId: section.Id, Ids: []string{page.Notifications[0].Id}})
	if err != nil {
		t.Fatal(err)
	}
	overview, err := pb.NewCommunityOverviewServiceClient(f.conn).GetCommunityOverview(f.owner, &pb.GetCommunityOverviewRequest{ChannelId: section.Id})
	if err != nil || len(overview.GetMentions()) != 0 {
		t.Fatal(overview, err)
	}
}

func TestPersonalNotificationsServerTimeoutDoesNotAcknowledge(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "Маша")
	mentionedChat(t, f, m, "timeout")
	page := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general"})
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err = tx.Exec(f.ctx, "SELECT id FROM personal_notifications WHERE id=$1 FOR UPDATE", page.Notifications[0].Id); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = pb.NewPersonalNotificationServiceClient(f.conn).MarkPersonalNotificationsRead(recipient, &pb.MarkPersonalNotificationsReadRequest{ChannelId: "general", Ids: []string{page.Notifications[0].Id}})
	if status.Code(err) != codes.DeadlineExceeded || time.Since(start) > 6*time.Second {
		t.Fatal(err, time.Since(start))
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", UnreadOnly: true}); len(p.Notifications) != 1 {
		t.Fatal(p)
	}
}
