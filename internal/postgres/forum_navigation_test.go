package postgres

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"sync"
	"testing"
)

func forumDevice(t *testing.T, f *channelFixture, root ed25519.PublicKey, private ed25519.PrivateKey, administrative bool) (context.Context, string) {
	t.Helper()
	device, key, _ := ed25519.GenerateKey(rand.Reader)
	grant := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device, Administrative: administrative}, private)
	login := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: grant.GrantId}, key)
	return metadata.AppendToOutgoingContext(f.ctx, "authorization", "Bearer "+login.AccessToken), grant.PrincipalId
}

func TestForumNavigationPinsAndDeviceAuthorization(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	section := forumSection(t, f, "navigation")
	root, key, _ := ed25519.GenerateKey(rand.Reader)
	limited, id := forumDevice(t, f, root, key, false)
	moderator, _ := forumDevice(t, f, root, key, true)
	stranger, _ := f.enroll(t, true)
	f.acl(t, section, roleRule("member", permissions(true, true, true, false)), principalRule(id, permissions(true, true, true, true)))
	first := createTopic(t, c, f.owner, section.Id, "first")
	second := createTopic(t, c, f.owner, section.Id, "second")
	third := createTopic(t, c, f.owner, section.Id, "third")
	pin := &pb.SetForumThreadPinnedRequest{ChannelId: section.Id, ThreadId: third.Id, Pinned: true, ExpectedRevision: third.Revision}
	for _, actor := range []context.Context{limited, stranger} {
		_, err := c.SetForumThreadPinned(actor, pin)
		requireCode(t, err, codes.PermissionDenied)
	}
	updated, err := c.SetForumThreadPinned(moderator, pin)
	if err != nil || !updated.Thread.Pinned {
		t.Fatalf("Закрепление: %v %v", updated, err)
	}
	_, err = c.SetForumThreadPinned(moderator, pin)
	requireCode(t, err, codes.Aborted)
	// Старая пагинация остаётся совместимой; новая выводит закрепления до обычных тем.
	legacy, err := c.ListForumThreads(limited, &pb.ListForumThreadsRequest{ChannelId: section.Id})
	if err != nil || legacy.Threads[0].Id != first.Id {
		t.Fatal(legacy, err)
	}
	page, err := c.ListForumThreads(limited, &pb.ListForumThreadsRequest{ChannelId: section.Id, PinnedFirst: true, Limit: 1})
	if err != nil || page.Threads[0].Id != third.Id || !page.NextPinned {
		t.Fatal(page, err)
	}
	next, err := c.ListForumThreads(limited, &pb.ListForumThreadsRequest{ChannelId: section.Id, PinnedFirst: true, AfterSequence: page.NextSequence, AfterPinned: page.NextPinned, Limit: 1})
	if err != nil || next.Threads[0].Id != first.Id || next.NextPinned {
		t.Fatal(next, err)
	}
	last, err := c.ListForumThreads(limited, &pb.ListForumThreadsRequest{ChannelId: section.Id, PinnedFirst: true, AfterSequence: next.NextSequence, AfterPinned: next.NextPinned, Limit: 1})
	if err != nil || last.Threads[0].Id != second.Id || last.NextSequence != 0 {
		t.Fatal(last, err)
	}
	var count int
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM admin_audit WHERE action=$1", "forum.pin:"+section.Id+":"+third.Id).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	f.acl(t, section, roleRule("member", permissions(true, true, true, false)))
	pin.ExpectedRevision = updated.Thread.Revision
	pin.Pinned = false
	_, err = c.SetForumThreadPinned(moderator, pin)
	requireCode(t, err, codes.PermissionDenied)
}

func TestForumReadStateAcrossDevicesAndRevocation(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	section := forumSection(t, f, "reading")
	root, key, _ := ed25519.GenerateKey(rand.Reader)
	one, id := forumDevice(t, f, root, key, false)
	two, _ := forumDevice(t, f, root, key, false)
	other, _ := f.enroll(t, false)
	f.acl(t, section, roleRule("member", permissions(true, true, false, false)))
	topic := createTopic(t, c, f.owner, section.Id, "topic")
	for _, key := range []string{"one", "two"} {
		_, err := c.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: key, IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
	}
	get := func(actor context.Context) *pb.ForumThread {
		r, err := c.GetForumThread(actor, &pb.GetForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id})
		if err != nil {
			t.Fatal(err)
		}
		return r.Thread
	}
	if before := get(one); before.Seen || before.UnreadCount != 3 {
		t.Fatal(before)
	}
	// Чтение страницы не меняет отметку автоматически.
	if get(two).Seen {
		t.Fatal("GET изменил отметку")
	}
	mark := &pb.MarkForumThreadReadRequest{ChannelId: section.Id, ThreadId: topic.Id, ThroughSequence: 1}
	r, err := c.MarkForumThreadRead(one, mark)
	if err != nil || !r.Thread.Seen || r.Thread.UnreadCount != 1 {
		t.Fatal(r, err)
	}
	if current := get(two); current.ReadSequence != 1 || current.UnreadCount != 1 {
		t.Fatal(current)
	}
	if get(other).Seen {
		t.Fatal("Отметка другого участника раскрыта")
	}
	mark.ThroughSequence = 3
	_, err = c.MarkForumThreadRead(one, mark)
	requireCode(t, err, codes.InvalidArgument)
	mark.ThroughSequence = -1
	_, err = c.MarkForumThreadRead(one, mark)
	requireCode(t, err, codes.InvalidArgument)
	var wg sync.WaitGroup
	for _, seq := range []int64{0, 2, 1, 0} {
		wg.Add(1)
		go func(seq int64) {
			defer wg.Done()
			_, err := c.MarkForumThreadRead(two, &pb.MarkForumThreadReadRequest{ChannelId: section.Id, ThreadId: topic.Id, ThroughSequence: seq})
			if err != nil {
				t.Error(err)
			}
		}(seq)
	}
	wg.Wait()
	if current := get(one); current.ReadSequence != 2 || current.UnreadCount != 0 {
		t.Fatal(current)
	}
	reopened, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var sequence int64
	if err = reopened.pool.QueryRow(f.ctx, "SELECT read_sequence FROM forum_read_state WHERE thread_id=$1 AND principal_id=$2", topic.Id, id).Scan(&sequence); err != nil || sequence != 2 {
		t.Fatal(sequence, err)
	}
	f.acl(t, section, roleRule("member", permissions(false, false, false, false)))
	_, err = c.MarkForumThreadRead(one, &pb.MarkForumThreadReadRequest{ChannelId: section.Id, ThreadId: topic.Id})
	requireCode(t, err, codes.NotFound)
	_, err = c.GetForumThread(two, &pb.GetForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id})
	requireCode(t, err, codes.NotFound)
	if _, err = f.channels.DeleteChannel(f.owner, &pb.DeleteChannelRequest{ChannelId: section.Id, ExpectedRevision: section.Revision}); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM forum_read_state WHERE thread_id=$1", topic.Id).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal(remaining, err)
	}
}

func TestForumNavigationUpgradeFromSchema27(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	section := forumSection(t, f, "upgrade_navigation")
	topic := createTopic(t, c, f.owner, section.Id, "legacy_topic")
	if _, err := c.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Сохранённый ответ", IdempotencyKey: "legacy_reply"}); err != nil {
		t.Fatal(err)
	}
	// Восстанавливаем именно структуру выпущенной схемы 27, сохраняя её строки и checksums.
	if _, err := f.store.pool.Exec(f.ctx, "DROP TABLE forum_read_state; ALTER TABLE forum_threads DROP COLUMN pinned; DELETE FROM schema_migrations WHERE version=28"); err != nil {
		t.Fatal(err)
	}
	upgraded, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var pinned bool
	var replies int64
	var title string
	if err = upgraded.pool.QueryRow(f.ctx, "SELECT title,pinned,reply_count FROM forum_threads WHERE id=$1", topic.Id).Scan(&title, &pinned, &replies); err != nil || title != topic.Title || pinned || replies != 1 {
		t.Fatal(title, pinned, replies, err)
	}
	var count int
	if err = upgraded.pool.QueryRow(f.ctx, "SELECT count(*) FROM forum_read_state").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
