package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func forumSection(t *testing.T, f *channelFixture, id string) *pb.Channel {
	t.Helper()
	r, err := f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: id, Title: "Обсуждения", ViewType: "forum"})
	if err != nil {
		t.Fatal(err)
	}
	return r.Channel
}
func createTopic(t *testing.T, c pb.ForumServiceClient, ctx context.Context, channel, key string) *pb.ForumThread {
	t.Helper()
	r, err := c.CreateForumThread(ctx, &pb.CreateForumThreadRequest{ChannelId: channel, Title: "Тема", Text: "Первое сообщение", IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return r.Thread
}
func TestForumPermissionsCloseAndReplay(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	section := forumSection(t, f, "forum")
	member, id := f.enroll(t, true)
	other, _ := f.enroll(t, false)
	f.acl(t, section, roleRule("member", permissions(true, true, true, false)))
	topic := createTopic(t, c, member, section.Id, "topic")
	same := createTopic(t, c, member, section.Id, "topic")
	if same.Id != topic.Id || topic.AuthorId != id {
		t.Fatal("Неверный автор или дубликат темы")
	}
	_, err := c.CreateForumThread(member, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Подмена", Text: topic.Text, IdempotencyKey: "topic"})
	requireCode(t, err, codes.AlreadyExists)
	_, err = c.CloseForumThread(member, &pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, ExpectedRevision: topic.Revision})
	requireCode(t, err, codes.PermissionDenied)
	r, err := c.ReplyForumThread(other, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Ответ", IdempotencyKey: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CloseForumThread(f.owner, &pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, ExpectedRevision: topic.Revision})
	requireCode(t, err, codes.Aborted)
	// Обычный участник становится модератором только после серверного ACL.
	f.acl(t, section, roleRule("member", permissions(true, true, true, false)), principalRule(id, permissions(true, true, true, true)))
	closed, err := c.CloseForumThread(member, &pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, ExpectedRevision: r.Thread.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if !closed.Thread.Closed || closed.Thread.ReplyCount != 1 {
		t.Fatal(closed)
	}
	for _, ctx := range []context.Context{member, other, f.owner} {
		_, err = c.ReplyForumThread(ctx, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Запрещено", IdempotencyKey: "after-close"})
		requireCode(t, err, codes.FailedPrecondition)
	}
	replay, err := c.ReplyForumThread(other, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Ответ", IdempotencyKey: "reply"})
	if err != nil || replay.Reply.Id != r.Reply.Id {
		t.Fatalf("Повтор ответа: %v %v", replay, err)
	}
	_, err = c.ReplyForumThread(other, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Подмена", IdempotencyKey: "reply"})
	requireCode(t, err, codes.AlreadyExists)
	f.acl(t, section, roleRule("member", permissions(true, true, true, false)))
	_, err = c.CloseForumThread(member, &pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, ExpectedRevision: closed.Thread.Revision})
	requireCode(t, err, codes.PermissionDenied)
	var n int
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM admin_audit WHERE action=$1", "forum.close:"+section.Id+":"+topic.Id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("Аудит: %d %v", n, err)
	}
	// Закрытие и после перезапуска хранилища остаётся действительным.
	reopened, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var closedInDB bool
	if err = reopened.pool.QueryRow(f.ctx, "SELECT closed FROM forum_threads WHERE id=$1", topic.Id).Scan(&closedInDB); err != nil || !closedInDB {
		t.Fatal(err)
	}
}
func TestForumIsolationPaginationAndValidation(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	section := forumSection(t, f, "forum")
	private := forumSection(t, f, "private")
	member, _ := f.enroll(t, true)
	f.acl(t, section, roleRule("member", permissions(true, true, false, false)))
	_, err := c.CreateForumThread(member, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Нет", Text: "Нет", IdempotencyKey: "key"})
	requireCode(t, err, codes.PermissionDenied)
	for i := 0; i < 3; i++ {
		createTopic(t, c, f.owner, section.Id, fmt.Sprint(i))
	}
	page, err := c.ListForumThreads(member, &pb.ListForumThreadsRequest{ChannelId: section.Id, Limit: 2})
	if err != nil || len(page.Threads) != 2 || page.NextSequence == 0 {
		t.Fatalf("Страница: %v %v", page, err)
	}
	next, err := c.ListForumThreads(member, &pb.ListForumThreadsRequest{ChannelId: section.Id, Limit: 2, AfterSequence: page.NextSequence})
	if err != nil || len(next.Threads) != 1 || next.NextSequence != 0 {
		t.Fatal(next, err)
	}
	topic := page.Threads[0]
	for i := 0; i < 3; i++ {
		if _, err = c.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: fmt.Sprint(i), IdempotencyKey: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := c.GetForumThread(member, &pb.GetForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Limit: 2})
	if err != nil || len(detail.Replies) != 2 || detail.NextSequence != 2 || detail.Thread.ReplyCount != 3 {
		t.Fatal(detail, err)
	}
	last, err := c.GetForumThread(member, &pb.GetForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, AfterSequence: detail.NextSequence})
	if err != nil || len(last.Replies) != 1 || last.NextSequence != 0 {
		t.Fatal(last, err)
	}
	_, err = c.GetForumThread(member, &pb.GetForumThreadRequest{ChannelId: private.Id, ThreadId: topic.Id})
	requireCode(t, err, codes.NotFound)
	_, err = c.GetForumThread(f.owner, &pb.GetForumThreadRequest{ChannelId: private.Id, ThreadId: topic.Id})
	requireCode(t, err, codes.NotFound)
	_, err = c.ListForumThreads(f.ctx, &pb.ListForumThreadsRequest{ChannelId: section.Id})
	requireCode(t, err, codes.Unauthenticated)
	_, err = c.ListForumThreads(member, &pb.ListForumThreadsRequest{ChannelId: section.Id, AfterSequence: -1})
	requireCode(t, err, codes.InvalidArgument)
	_, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: section.Id, Text: "Обход", IdempotencyKey: "bypass"})
	requireCode(t, err, codes.FailedPrecondition)
	_, err = c.ListForumThreads(f.owner, &pb.ListForumThreadsRequest{ChannelId: "general"})
	requireCode(t, err, codes.FailedPrecondition)
	for _, req := range []*pb.CreateForumThreadRequest{
		{ChannelId: section.Id, Title: " ", Text: "x", IdempotencyKey: "x"},
		{ChannelId: section.Id, Title: "x", Text: " ", IdempotencyKey: "x"},
		{ChannelId: section.Id, Title: "x", Text: "x"},
	} {
		_, err = c.CreateForumThread(f.owner, req)
		requireCode(t, err, codes.InvalidArgument)
	}
	// Удаление раздела каскадно удаляет темы и ответы.
	_, err = f.channels.DeleteChannel(f.owner, &pb.DeleteChannelRequest{ChannelId: section.Id, ExpectedRevision: section.Revision})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM forum_replies WHERE channel_id=$1", section.Id).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
func TestForumReplyCloseRace(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	section := forumSection(t, f, "race")
	for i := 0; i < 8; i++ {
		topic := createTopic(t, c, f.owner, section.Id, fmt.Sprint(i))
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var replyErr, closeErr error
		go func() {
			defer wg.Done()
			<-start
			_, replyErr = c.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Ответ", IdempotencyKey: fmt.Sprint(i)})
		}()
		go func() {
			defer wg.Done()
			<-start
			_, closeErr = c.CloseForumThread(f.owner, &pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, ExpectedRevision: topic.Revision})
		}()
		close(start)
		wg.Wait()
		if closeErr != nil {
			requireCode(t, closeErr, codes.Aborted)
			if replyErr != nil {
				t.Fatal(replyErr)
			}
			_, err := c.CloseForumThread(f.owner, &pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, ExpectedRevision: topic.Revision + 1})
			if err != nil {
				t.Fatal(err)
			}
		} else if status.Code(replyErr) != codes.FailedPrecondition {
			t.Fatalf("Ответ обошёл закрытие: %v", replyErr)
		}
		_, err := c.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Поздний", IdempotencyKey: fmt.Sprint("late", i)})
		requireCode(t, err, codes.FailedPrecondition)
	}
}
func TestForumConnectUsesServerACL(t *testing.T) {
	f := newChannelFixture(t)
	section := forumSection(t, f, "connect_forum")
	server := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer server.Close()
	c := rpc.NewForumServiceClient(server.Client(), server.URL)
	member, _ := f.enroll(t, true)
	f.acl(t, section, roleRule("member", permissions(true, true, true, false)))
	md, _ := metadata.FromOutgoingContext(member)
	req := connect.NewRequest(&pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Через Connect", Text: "Текст", IdempotencyKey: "connect"})
	req.Header().Set("Authorization", md.Get("authorization")[0])
	topic, err := c.CreateForumThread(f.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	closeReq := connect.NewRequest(&pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Msg.Thread.Id, ExpectedRevision: 1})
	closeReq.Header().Set("Authorization", md.Get("authorization")[0])
	if _, err = c.CloseForumThread(f.ctx, closeReq); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	if _, err = c.ListForumThreads(f.ctx, connect.NewRequest(&pb.ListForumThreadsRequest{ChannelId: section.Id})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal(err)
	}
}

func TestForumUpgradeFromReleasedSchema25(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	original := seedVersion12(t, ctx, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for i := 12; i < 25; i++ {
		if err = applyMigration(ctx, tx, i+1, migrationFiles[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO channels(id,title,view_type,voice_capacity) VALUES('voice32','Комната','voice_room',32)"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before := legacySnapshot(t, ctx, pool)
	store, identity, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if original.ServerID != identity.ServerID {
		t.Fatal("Миграция сменила идентичность")
	}
	after := legacySnapshot(t, ctx, pool)
	for table, value := range before {
		if value != after[table] {
			t.Errorf("Изменились данные %s", table)
		}
	}
	var capacity, version, count int
	if err = pool.QueryRow(ctx, "SELECT voice_capacity,(SELECT max(version) FROM schema_migrations),(SELECT count(*) FROM forum_threads)+(SELECT count(*) FROM forum_replies) FROM channels WHERE id='voice32'").Scan(&capacity, &version, &count); err != nil {
		t.Fatal(err)
	}
	if capacity != 32 || version != SchemaVersion() || count != 0 {
		t.Fatal(capacity, version, count)
	}
}
func TestForumDeviceScopeReaderBlockAndArchive(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	section := forumSection(t, f, "permissions")
	member, id := f.enroll(t, false)
	f.acl(t, section, principalRule(id, permissions(true, true, true, true)))
	topic := createTopic(t, c, member, section.Id, "topic")
	_, err := c.CloseForumThread(member, &pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, ExpectedRevision: 1})
	requireCode(t, err, codes.PermissionDenied)
	// Глобальная роль reader запрещает запись даже при явном can_write в ACL.
	var revision int64
	if err = f.store.pool.QueryRow(f.ctx, "SELECT revision FROM memberships WHERE principal_id=$1", id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	changed, err := f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: id, Role: "reader", ExpectedRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ReplyForumThread(member, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Нет", IdempotencyKey: "reader"})
	requireCode(t, err, codes.PermissionDenied)
	_, err = f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: id, Role: "reader", Blocked: true, ExpectedRevision: changed.Member.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GetForumThread(member, &pb.GetForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id})
	requireCode(t, err, codes.PermissionDenied)
	_, err = f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: section.Id, Title: section.Title, Archived: true, ExpectedRevision: section.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CloseForumThread(f.owner, &pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, ExpectedRevision: 1})
	requireCode(t, err, codes.FailedPrecondition)
	_, err = c.GetForumThread(f.owner, &pb.GetForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id})
	if err != nil {
		t.Fatal(err)
	}
}

func forumFile(t *testing.T, f *channelFixture, actor context.Context, channel, key string) *pb.Asset {
	t.Helper()
	client := pb.NewAssetServiceClient(f.conn)
	data := []byte("forum attachment " + key)
	hash := sha256.Sum256(data)
	started, err := client.BeginUpload(actor, &pb.BeginUploadRequest{ChannelId: channel, Name: key + ".txt", ByteSize: int64(len(data)), Sha256: hash[:], IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.UploadChunk(actor, &pb.UploadChunkRequest{AssetId: started.Asset.Id, Data: data}); err != nil {
		t.Fatal(err)
	}
	ready, err := client.CompleteUpload(actor, &pb.CompleteUploadRequest{AssetId: started.Asset.Id})
	if err != nil {
		t.Fatal(err)
	}
	return ready.Asset
}
func TestForumAttachmentsPublicationACLAndClose(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	files := pb.NewAssetServiceClient(f.conn)
	section := forumSection(t, f, "files_forum")
	member, id := f.enroll(t, true)
	f.acl(t, section, roleRule("member", permissions(true, true, true, false)))
	root := forumFile(t, f, f.owner, section.Id, "root")
	made, err := c.CreateForumThread(f.owner, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Файлы", AttachmentIds: []string{root.Id}, IdempotencyKey: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if len(made.Thread.Attachments) != 1 || made.Thread.Text != "" {
		t.Fatal(made)
	}
	own := forumFile(t, f, member, section.Id, "own")
	// Чужой файл и уже опубликованный файл не могут быть присвоены ответу.
	_, err = c.ReplyForumThread(member, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: made.Thread.Id, AttachmentIds: []string{root.Id}, IdempotencyKey: "steal"})
	requireCode(t, err, codes.PermissionDenied)
	req := &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: made.Thread.Id, AttachmentIds: []string{own.Id}, IdempotencyKey: "reply"}
	reply, err := c.ReplyForumThread(member, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Reply.Attachments) != 1 {
		t.Fatal(reply)
	}
	replay, err := c.ReplyForumThread(member, req)
	if err != nil || replay.Reply.Id != reply.Reply.Id {
		t.Fatal(replay, err)
	}
	_, err = c.ReplyForumThread(member, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: made.Thread.Id, AttachmentIds: []string{own.Id}, IdempotencyKey: "reuse"})
	requireCode(t, err, codes.FailedPrecondition)
	_, err = files.CancelUpload(member, &pb.CancelUploadRequest{AssetId: own.Id})
	requireCode(t, err, codes.FailedPrecondition)
	data, err := files.DownloadChunk(f.owner, &pb.DownloadChunkRequest{AssetId: own.Id})
	if err != nil || !bytes.Equal(data.Data, []byte("forum attachment own")) {
		t.Fatal(data, err)
	}
	detail, err := c.GetForumThread(member, &pb.GetForumThreadRequest{ChannelId: section.Id, ThreadId: made.Thread.Id})
	if err != nil || len(detail.Thread.Attachments) != 1 || len(detail.Replies[0].Attachments) != 1 {
		t.Fatal(detail, err)
	}
	// Даже загруженный файл не позволяет обойти закрытие.
	pending := forumFile(t, f, member, section.Id, "pending")
	_, err = c.CloseForumThread(f.owner, &pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: made.Thread.Id, ExpectedRevision: reply.Thread.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ReplyForumThread(member, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: made.Thread.Id, AttachmentIds: []string{pending.Id}, IdempotencyKey: "closed"})
	requireCode(t, err, codes.FailedPrecondition)
	_, err = files.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: pending.Id})
	requireCode(t, err, codes.NotFound)
	// GC незавершённых загрузок сохраняет опубликованные форумные файлы.
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE assets SET created_at_unix_ms=1 WHERE id=$1", own.Id); err != nil {
		t.Fatal(err)
	}
	forumFile(t, f, f.owner, "general", "trigger_gc")
	_, err = files.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: own.Id})
	if err != nil {
		t.Fatal(err)
	}
	f.acl(t, section, principalRule(id, permissions(false, false, false, false)))
	_, err = files.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: own.Id})
	requireCode(t, err, codes.NotFound)
	_, err = f.channels.DeleteChannel(f.owner, &pb.DeleteChannelRequest{ChannelId: section.Id, ExpectedRevision: section.Revision})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM assets WHERE channel_id=$1", section.Id).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
