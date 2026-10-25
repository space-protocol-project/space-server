package postgres

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"testing"
	"time"
)

const dropPersonalNotificationsSchema = `DROP VIEW personal_notification_content;
 DROP TRIGGER personal_notifications_chat ON contents;
 DROP TRIGGER personal_notifications_forum_thread ON forum_threads;
 DROP TRIGGER personal_notifications_forum_reply ON forum_replies;
 DROP TRIGGER personal_notifications_feed_post ON feed_posts;
 DROP TRIGGER personal_notifications_feed_comment ON feed_comments;
 DROP FUNCTION space_capture_notifications_v1();
 DROP FUNCTION space_record_notification_v1(text,text,text,integer,text,integer,bigint);
 DROP TABLE personal_notifications; DROP TABLE personal_notification_streams;
 DELETE FROM schema_migrations WHERE version=36;`

func notificationPage(t *testing.T, f *channelFixture, actor context.Context, req *pb.ListPersonalNotificationsRequest) *pb.ListPersonalNotificationsResponse {
	t.Helper()
	page, err := pb.NewPersonalNotificationServiceClient(f.conn).ListPersonalNotifications(actor, req)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func mentionedChat(t *testing.T, f *channelFixture, m *pb.CommunityMention, key string) *pb.Content {
	t.Helper()
	result, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "@" + m.DisplayName, IdempotencyKey: key, Mentions: []*pb.CommunityMention{m}})
	if err != nil {
		t.Fatal(err)
	}
	return result.Content
}

func TestPersonalNotificationsChatPagesAndAtomicRead(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "Маша")
	other, _ := f.enroll(t, true)
	first := mentionedChat(t, f, m, "one")
	mentionedChat(t, f, m, "two")
	api := pb.NewPersonalNotificationServiceClient(f.conn)
	page := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", Limit: 1, UnreadOnly: true})
	if !page.HasMore || len(page.Notifications) != 1 || page.ThroughSequence != 2 || page.NextSequence != 1 {
		t.Fatal(page)
	}
	notice := page.Notifications[0]
	if notice.ContentId != first.Id || notice.TargetSequence != first.Sequence || notice.Reasons != 1 || notice.UnreadReasons != 1 || notice.TargetKind != pb.NotificationTargetKind_NOTIFICATION_TARGET_KIND_CHAT_MESSAGE {
		t.Fatal(notice)
	}
	mentionedChat(t, f, m, "three")
	second := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", AfterSequence: page.NextSequence, ThroughSequence: page.ThroughSequence, Limit: 1, UnreadOnly: true})
	if second.HasMore || len(second.Notifications) != 1 || second.NextSequence != 2 || second.ThroughSequence != 2 {
		t.Fatal(second)
	}
	late := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", AfterSequence: second.NextSequence})
	if len(late.Notifications) != 1 || late.NextSequence != 3 {
		t.Fatal(late)
	}
	_, err := api.MarkPersonalNotificationsRead(recipient, &pb.MarkPersonalNotificationsReadRequest{ChannelId: "general", Ids: []string{notice.Id, "unknown"}})
	requireCode(t, err, codes.NotFound)
	if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", UnreadOnly: true}); len(p.Notifications) != 3 {
		t.Fatal(p)
	}
	_, err = api.MarkPersonalNotificationsRead(other, &pb.MarkPersonalNotificationsReadRequest{ChannelId: "general", Ids: []string{notice.Id}})
	requireCode(t, err, codes.NotFound)
	for range 2 {
		if _, err = api.MarkPersonalNotificationsRead(recipient, &pb.MarkPersonalNotificationsReadRequest{ChannelId: "general", Ids: []string{notice.Id, notice.Id}}); err != nil {
			t.Fatal(err)
		}
	}
	if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", UnreadOnly: true}); len(p.Notifications) != 2 {
		t.Fatal(p)
	}
	if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", BaselineOnly: true}); len(p.Notifications) != 0 || p.NextSequence != 3 {
		t.Fatal(p)
	}
	// Свободный текст и повтор принятой операции не создают новую запись.
	if _, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "@Маша", IdempotencyKey: "plain"}); err != nil {
		t.Fatal(err)
	}
	mentionedChat(t, f, m, "one")
	if p := notificationPage(t, f, other, &pb.ListPersonalNotificationsRequest{ChannelId: "general"}); len(p.Notifications) != 0 || p.ThroughSequence != 0 {
		t.Fatal(p)
	}
	if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", BaselineOnly: true}); p.NextSequence != 3 {
		t.Fatal(p)
	}
}

func TestPersonalNotificationsChatEditPreservesReadAndRemoval(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "Маша")
	message := mentionedChat(t, f, m, "edit")
	api := pb.NewPersonalNotificationServiceClient(f.conn)
	before := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general"}).Notifications[0]
	if _, err := api.MarkPersonalNotificationsRead(recipient, &pb.MarkPersonalNotificationsReadRequest{ChannelId: "general", Ids: []string{before.Id}}); err != nil {
		t.Fatal(err)
	}
	removed, err := f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: message.Id, ExpectedRevision: 1, Text: "без обращения"})
	if err != nil {
		t.Fatal(err)
	}
	if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general"}); len(p.Notifications) != 0 || p.NextSequence != 1 {
		t.Fatal(p)
	}
	restored, err := f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: message.Id, ExpectedRevision: removed.Content.Revision, Text: "@Маша", Mentions: []*pb.CommunityMention{m}})
	if err != nil {
		t.Fatal(err)
	}
	after := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general"})
	if len(after.Notifications) != 1 || after.Notifications[0].Id != before.Id || after.Notifications[0].UnreadReasons != 0 || after.ThroughSequence != 1 {
		t.Fatal(after)
	}
	if _, err = f.contents.DeleteContent(f.owner, &pb.DeleteContentRequest{ChannelId: "general", Id: message.Id, ExpectedRevision: restored.Content.Revision}); err != nil {
		t.Fatal(err)
	}
	if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general"}); len(p.Notifications) != 0 {
		t.Fatal(p)
	}
}

func TestPersonalNotificationsForumReasonsAndHiddenParent(t *testing.T) {
	f := newChannelFixture(t)
	author, _ := f.enroll(t, true)
	if _, err := f.store.pool.Exec(f.ctx, "UPDATE memberships SET nickname='Автор' WHERE principal_id=$1", f.ownerID); err != nil {
		t.Fatal(err)
	}
	section := forumSection(t, f, "notifications_forum")
	api := pb.NewForumServiceClient(f.conn)
	topic := createTopic(t, api, f.owner, section.Id, "topic")
	reply, err := api.ReplyForumThread(author, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "@Автор", IdempotencyKey: "reply", Mentions: []*pb.CommunityMention{{PrincipalId: f.ownerID, DisplayName: "Автор", Length: 6}}})
	if err != nil {
		t.Fatal(err)
	}
	page := notificationPage(t, f, f.owner, &pb.ListPersonalNotificationsRequest{ChannelId: section.Id})
	if len(page.Notifications) != 1 || page.Notifications[0].Reasons != 3 || page.Notifications[0].UnreadReasons != 3 || page.Notifications[0].ContentId != reply.Reply.Id || page.Notifications[0].ParentId != topic.Id {
		t.Fatal(page)
	}
	id := page.Notifications[0].Id
	_, err = pb.NewCommunityMentionServiceClient(f.conn).MarkCommunityMentionsRead(f.owner, &pb.MarkCommunityMentionsReadRequest{ChannelId: section.Id, Contents: []*pb.CommunityContentReference{{Kind: pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FORUM_REPLY, Id: reply.Reply.Id}}})
	if err != nil {
		t.Fatal(err)
	}
	if p := notificationPage(t, f, f.owner, &pb.ListPersonalNotificationsRequest{ChannelId: section.Id, UnreadOnly: true}); len(p.Notifications) != 1 || p.Notifications[0].UnreadReasons != 2 {
		t.Fatal(p)
	}
	if _, err = api.MarkForumThreadRead(f.owner, &pb.MarkForumThreadReadRequest{ChannelId: section.Id, ThreadId: topic.Id, ThroughSequence: 1}); err != nil {
		t.Fatal(err)
	}
	if p := notificationPage(t, f, f.owner, &pb.ListPersonalNotificationsRequest{ChannelId: section.Id, UnreadOnly: true}); len(p.Notifications) != 0 {
		t.Fatal(p)
	}
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE forum_threads SET hidden=true WHERE id=$1", topic.Id); err != nil {
		t.Fatal(err)
	}
	if p := notificationPage(t, f, f.owner, &pb.ListPersonalNotificationsRequest{ChannelId: section.Id}); len(p.Notifications) != 0 {
		t.Fatal(p)
	}
	_, err = pb.NewPersonalNotificationServiceClient(f.conn).MarkPersonalNotificationsRead(f.owner, &pb.MarkPersonalNotificationsReadRequest{ChannelId: section.Id, Ids: []string{id}})
	requireCode(t, err, codes.NotFound)
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE forum_threads SET hidden=false WHERE id=$1", topic.Id); err != nil {
		t.Fatal(err)
	}
	if p := notificationPage(t, f, f.owner, &pb.ListPersonalNotificationsRequest{ChannelId: section.Id}); len(p.Notifications) != 1 || p.Notifications[0].Id != id || p.ThroughSequence != 1 {
		t.Fatal(p)
	}
}

func TestPersonalNotificationsLateCommitCannotPassCursor(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "Маша")
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	// Выделяем номер без фиксации. Вторая транзакция не должна обогнать его.
	record := func(tx pgx.Tx, id string) error {
		_, e := tx.Exec(f.ctx, "SELECT space_record_notification_v1('general',$1,$2,5,$3,1,1)", m.PrincipalId, f.ownerID, id)
		return e
	}
	if err = record(tx, "pending-one"); err != nil {
		t.Fatal(err)
	}
	tx2, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback(f.ctx)
	done := make(chan error, 1)
	go func() { done <- record(tx2, "pending-two") }()
	// Наблюдаем настоящую блокировку PostgreSQL, без предположений о скорости goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for {
		var waiting bool
		if err = f.store.pool.QueryRow(f.ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND NOT granted)", tx2.Conn().PgConn().PID()).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Вторая запись не ожидала первую транзакцию")
		}
		time.Sleep(time.Millisecond)
	}
	if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", BaselineOnly: true}); p.ThroughSequence != 0 {
		t.Fatal(p)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", BaselineOnly: true}); p.ThroughSequence != 1 {
		t.Fatal(p)
	}
	if err = tx2.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", BaselineOnly: true}); p.ThroughSequence != 2 {
		t.Fatal(p)
	}
}

func TestPersonalNotificationsBudgetIncludesForumAndChat(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "Маша")
	section := forumSection(t, f, "budget_notifications")
	api := pb.NewForumServiceClient(f.conn)
	for i := range 50 {
		_, err := api.CreateForumThread(f.owner, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Обращение", Text: "@Маша", IdempotencyKey: fmt.Sprint(i), Mentions: []*pb.CommunityMention{m}})
		if err != nil {
			t.Fatal(err)
		}
		mentionedChat(t, f, m, fmt.Sprint(i))
	}
	_, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "@Маша", IdempotencyKey: "over-budget", Mentions: []*pb.CommunityMention{m}})
	requireCode(t, err, codes.ResourceExhausted)
	_, err = api.CreateForumThread(f.owner, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Сверх лимита", Text: "@Маша", IdempotencyKey: "over-budget", Mentions: []*pb.CommunityMention{m}})
	requireCode(t, err, codes.ResourceExhausted)
	original := mentionedChat(t, f, m, "0")
	_, other := mentionRecipient(t, f, "Ира")
	_, err = f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: original.Id, ExpectedRevision: 1, Text: "@Ира", Mentions: []*pb.CommunityMention{other}})
	requireCode(t, err, codes.ResourceExhausted)
	if current := mentionedChat(t, f, m, "0"); current.Text != "@Маша" {
		t.Fatal(current)
	}
	for _, channel := range []string{"general", section.Id} {
		if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: channel, BaselineOnly: true}); p.ThroughSequence != 50 {
			t.Fatal(p)
		}
	}
}

func TestPersonalNotificationsUpgradeAndDevices(t *testing.T) {
	f := newChannelFixture(t)
	root, key, _ := ed25519.GenerateKey(rand.Reader)
	one, id := forumDevice(t, f, root, key, true)
	two, _ := forumDevice(t, f, root, key, true)
	if _, err := f.store.pool.Exec(f.ctx, "UPDATE memberships SET nickname='Маша' WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	m := &pb.CommunityMention{PrincipalId: id, DisplayName: "Маша", Length: 5}
	message := mentionedChat(t, f, m, "legacy")
	if _, err := f.store.pool.Exec(f.ctx, dropPersonalNotificationsSchema); err != nil {
		t.Fatal(err)
	}
	before := legacySnapshot(t, f.ctx, f.store.pool)
	upgraded, identity, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if identity.ServerID != f.identity.ServerID {
		t.Fatal("Изменилась идентичность")
	}
	after := legacySnapshot(t, f.ctx, upgraded.pool)
	for table, value := range before {
		if after[table] != value {
			t.Fatal("Изменены прежние данные", table)
		}
	}
	page := notificationPage(t, f, one, &pb.ListPersonalNotificationsRequest{ChannelId: "general"})
	if len(page.Notifications) != 1 || page.Notifications[0].ContentId != message.Id {
		t.Fatal(page)
	}
	if _, err = pb.NewPersonalNotificationServiceClient(f.conn).MarkPersonalNotificationsRead(two, &pb.MarkPersonalNotificationsReadRequest{ChannelId: "general", Ids: []string{page.Notifications[0].Id}}); err != nil {
		t.Fatal(err)
	}
	if p := notificationPage(t, f, one, &pb.ListPersonalNotificationsRequest{ChannelId: "general", UnreadOnly: true}); len(p.Notifications) != 0 {
		t.Fatal(p)
	}
	reopened, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var read bool
	if err = reopened.pool.QueryRow(f.ctx, "SELECT read_at IS NOT NULL FROM personal_notifications WHERE id=$1", page.Notifications[0].Id).Scan(&read); err != nil || !read {
		t.Fatal(read, err)
	}
}
func TestPersonalNotificationsChatVisibleRead(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "Маша")
	other, _ := f.enroll(t, true)
	message := mentionedChat(t, f, m, "visible")
	api := pb.NewPersonalNotificationServiceClient(f.conn)
	page := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", UnreadOnly: true})
	for _, req := range []*pb.MarkPersonalNotificationsReadRequest{
		{ChannelId: "general", ChatContentIds: []string{message.Id, "unknown"}},
		{ChannelId: "general", ChatContentIds: []string{message.Id}, Ids: []string{page.Notifications[0].Id}},
	} {
		_, err := api.MarkPersonalNotificationsRead(recipient, req)
		if len(req.Ids) > 0 {
			requireCode(t, err, codes.InvalidArgument)
		} else {
			requireCode(t, err, codes.NotFound)
		}
	}
	_, err := api.MarkPersonalNotificationsRead(other, &pb.MarkPersonalNotificationsReadRequest{ChannelId: "general", ChatContentIds: []string{message.Id}})
	requireCode(t, err, codes.NotFound)
	if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", UnreadOnly: true}); len(p.Notifications) != 1 {
		t.Fatal(p)
	}
	for range 2 {
		_, err = api.MarkPersonalNotificationsRead(recipient, &pb.MarkPersonalNotificationsReadRequest{ChannelId: "general", ChatContentIds: []string{message.Id, message.Id}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general", UnreadOnly: true}); len(p.Notifications) != 0 {
		t.Fatal(p)
	}
}
