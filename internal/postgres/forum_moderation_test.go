package postgres

import (
	"connectrpc.com/connect"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestForumModerationConnectAndRevocation(t *testing.T) {
	f := newChannelFixture(t)
	section := forumSection(t, f, "moderation_connect")
	grpcClient := pb.NewForumServiceClient(f.conn)
	topic := createTopic(t, grpcClient, f.owner, section.Id, "connect")
	reply, err := grpcClient.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Ответ", IdempotencyKey: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := grpcClient.CloseForumThread(f.owner, &pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, ExpectedRevision: reply.Thread.Revision})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer server.Close()
	c := rpc.NewForumServiceClient(server.Client(), server.URL)
	md, _ := metadata.FromOutgoingContext(f.owner)
	token := md.Get("authorization")[0]
	req := connect.NewRequest(&pb.ModerateForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Action: "reopen", Reason: "Продолжаем", ExpectedRevision: closed.Thread.Revision})
	req.Header().Set("Authorization", token)
	opened, err := c.ModerateForumThread(f.ctx, req)
	if err != nil || opened.Msg.Thread.Closed {
		t.Fatal(opened, err)
	}
	hide := connect.NewRequest(&pb.ModerateForumReplyRequest{ChannelId: section.Id, ThreadId: topic.Id, ReplyId: reply.Reply.Id, Hidden: true, Reason: "Проверка", ExpectedRevision: reply.Reply.Revision})
	hide.Header().Set("Authorization", token)
	hidden, err := c.ModerateForumReply(f.ctx, hide)
	if err != nil || !hidden.Msg.Reply.Hidden || hidden.Msg.Reply.Text != "" {
		t.Fatal(hidden, err)
	}
	log := connect.NewRequest(&pb.ListForumModerationRequest{ChannelId: section.Id, ThreadId: topic.Id})
	log.Header().Set("Authorization", token)
	page, err := c.ListForumModeration(f.ctx, log)
	if err != nil || len(page.Msg.Events) != 2 {
		t.Fatal(page, err)
	}
	member, _ := f.enroll(t, true)
	f.acl(t, section, roleRule("member", permissions(true, true, true, false)))
	memberMD, _ := metadata.FromOutgoingContext(member)
	log.Header().Set("Authorization", memberMD.Get("authorization")[0])
	_, err = c.ListForumModeration(f.ctx, log)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	// Архивирование запрещает новые действия даже владельцу.
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE channels SET archived=true WHERE id=$1", section.Id); err != nil {
		t.Fatal(err)
	}
	req.Msg.Action = "hide"
	req.Msg.ExpectedRevision = hidden.Msg.Thread.Revision
	_, err = c.ModerateForumThread(f.ctx, req)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal(err)
	}
}

func TestForumModerationLifecycleAssetsAndAudit(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	files := pb.NewAssetServiceClient(f.conn)
	section := forumSection(t, f, "moderation")
	root, key, _ := ed25519.GenerateKey(rand.Reader)
	limited, id := forumDevice(t, f, root, key, false)
	moderator, _ := forumDevice(t, f, root, key, true)
	member, _ := f.enroll(t, true)
	f.acl(t, section, roleRule("member", permissions(true, true, true, false)), principalRule(id, permissions(true, true, true, true)))
	asset := forumFile(t, f, f.owner, section.Id, "original")
	create := &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Оригинальный заголовок", Text: "Исходный текст", AttachmentIds: []string{asset.Id}, IdempotencyKey: "original"}
	made, err := c.CreateForumThread(f.owner, create)
	if err != nil {
		t.Fatal(err)
	}
	attachment := forumFile(t, f, member, section.Id, "answer")
	answer := &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: made.Thread.Id, Text: "Исходный ответ", AttachmentIds: []string{attachment.Id}, IdempotencyKey: "answer"}
	reply, err := c.ReplyForumThread(member, answer)
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.ModerateForumReplyRequest{ChannelId: section.Id, ThreadId: made.Thread.Id, ReplyId: reply.Reply.Id, Hidden: true, Reason: "Персональные данные", ExpectedRevision: reply.Reply.Revision}
	for _, actor := range []context.Context{limited, member} {
		_, err = c.ModerateForumReply(actor, req)
		requireCode(t, err, codes.PermissionDenied)
	}
	hidden, err := c.ModerateForumReply(moderator, req)
	if err != nil || !hidden.Reply.Hidden || hidden.Reply.Text != "" || len(hidden.Reply.Attachments) != 0 {
		t.Fatal(hidden, err)
	}
	_, err = c.ModerateForumReply(moderator, req)
	requireCode(t, err, codes.Aborted)
	replay, err := c.ReplyForumThread(member, answer)
	if err != nil || replay.Reply.Text != "" || replay.Reply.AuthorId != "" || len(replay.Reply.Attachments) != 0 {
		t.Fatal(replay, err)
	}
	for _, actor := range []context.Context{f.owner, moderator, member} {
		_, err = files.DownloadChunk(actor, &pb.DownloadChunkRequest{AssetId: attachment.Id})
		requireCode(t, err, codes.NotFound)
	}
	own, err := files.ListMyAssets(member, &pb.ListMyAssetsRequest{ChannelId: section.Id})
	if err != nil || len(own.Assets) != 0 {
		t.Fatal(own, err)
	}
	catalog, err := files.ListChannelAssets(member, &pb.ListChannelAssetsRequest{ChannelId: section.Id})
	_, authorVisible := catalog.GetAuthors()[reply.Reply.AuthorId]
	if err != nil || len(catalog.Assets) != 1 || authorVisible {
		t.Fatal(catalog, err)
	}
	// Скрытие родителя блокирует и видимые, и скрытые дочерние файлы.
	hide := &pb.ModerateForumThreadRequest{ChannelId: section.Id, ThreadId: made.Thread.Id, Action: "hide", Reason: "Проверка правил", ExpectedRevision: hidden.Thread.Revision}
	h, err := c.ModerateForumThread(moderator, hide)
	if err != nil || !h.Thread.Hidden || h.Thread.Title != "" || h.Thread.Text != "" || len(h.Thread.Attachments) != 0 {
		t.Fatal(h, err)
	}
	duplicate, err := c.CreateForumThread(f.owner, create)
	if err != nil || duplicate.Thread.Title != "" || len(duplicate.Thread.Attachments) != 0 {
		t.Fatal(duplicate, err)
	}
	detail, err := c.GetForumThread(member, &pb.GetForumThreadRequest{ChannelId: section.Id, ThreadId: h.Thread.Id})
	if err != nil || detail.Replies[0].Text != "" {
		t.Fatal(detail, err)
	}
	list, err := c.ListForumThreads(member, &pb.ListForumThreadsRequest{ChannelId: section.Id})
	if err != nil || list.Threads[0].Title != "" {
		t.Fatal(list, err)
	}
	_, err = c.ReplyForumThread(member, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: h.Thread.Id, Text: "Новый", IdempotencyKey: "blocked"})
	requireCode(t, err, codes.FailedPrecondition)
	_, err = files.DownloadChunk(f.owner, &pb.DownloadChunkRequest{AssetId: asset.Id})
	requireCode(t, err, codes.NotFound)
	catalog, err = files.ListChannelAssets(f.owner, &pb.ListChannelAssetsRequest{ChannelId: section.Id})
	if err != nil || len(catalog.Assets) != 0 || len(catalog.Authors) != 0 {
		t.Fatal(catalog, err)
	}
	hash := sha256.Sum256([]byte("forum attachment original"))
	_, err = files.BeginUpload(f.owner, &pb.BeginUploadRequest{ChannelId: section.Id, Name: "original.txt", ByteSize: int64(len("forum attachment original")), Sha256: hash[:], IdempotencyKey: "original"})
	requireCode(t, err, codes.NotFound)
	_, err = files.CompleteUpload(f.owner, &pb.CompleteUploadRequest{AssetId: asset.Id})
	requireCode(t, err, codes.NotFound)
	_, err = files.ListChannelAssets(f.owner, &pb.ListChannelAssetsRequest{ChannelId: section.Id, After: asset.Id})
	requireCode(t, err, codes.InvalidArgument)
	// Восстановление темы не восстанавливает независимо скрытый ответ.
	hide.Action = "restore"
	hide.ExpectedRevision = h.Thread.Revision
	hide.Reason = "Проверка завершена"
	restored, err := c.ModerateForumThread(moderator, hide)
	if err != nil || restored.Thread.Text != create.Text || len(restored.Thread.Attachments) != 1 {
		t.Fatal(restored, err)
	}
	_, err = files.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: attachment.Id})
	requireCode(t, err, codes.NotFound)
	req.Hidden = false
	req.ExpectedRevision = hidden.Reply.Revision
	req.Reason = "Данные исправлены"
	shown, err := c.ModerateForumReply(moderator, req)
	if err != nil || shown.Reply.Text != answer.Text {
		t.Fatal(shown, err)
	}
	if _, err = files.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: attachment.Id}); err != nil {
		t.Fatal(err)
	}
	closed, err := c.CloseForumThread(moderator, &pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: made.Thread.Id, ExpectedRevision: shown.Thread.Revision})
	if err != nil {
		t.Fatal(err)
	}
	reopen := &pb.ModerateForumThreadRequest{ChannelId: section.Id, ThreadId: made.Thread.Id, Action: "reopen", Reason: "Продолжаем обсуждение", ExpectedRevision: closed.Thread.Revision}
	opened, err := c.ModerateForumThread(moderator, reopen)
	if err != nil || opened.Thread.Closed {
		t.Fatal(opened, err)
	}
	if _, err = c.ReplyForumThread(member, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: made.Thread.Id, Text: "Снова пишем", IdempotencyKey: "reopened"}); err != nil {
		t.Fatal(err)
	}
	logReq := &pb.ListForumModerationRequest{ChannelId: section.Id, ThreadId: made.Thread.Id, Limit: 2}
	for _, actor := range []context.Context{limited, member} {
		_, err = c.ListForumModeration(actor, logReq)
		requireCode(t, err, codes.PermissionDenied)
	}
	total := 0
	for {
		page, err := c.ListForumModeration(moderator, logReq)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			if e.ActorId != id || e.Reason == "" || e.Revision <= 1 {
				t.Fatal(e)
			}
		}
		total += len(page.Events)
		if page.NextId == 0 {
			break
		}
		logReq.AfterId = page.NextId
	}
	if total != 5 {
		t.Fatal(total)
	}
	f.acl(t, section, roleRule("member", permissions(true, true, true, false)))
	_, err = c.ListForumModeration(moderator, logReq)
	requireCode(t, err, codes.PermissionDenied)
}

func TestForumModerationValidationAtomicityAndRace(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	section := forumSection(t, f, "moderation_race")
	topic := createTopic(t, c, f.owner, section.Id, "race")
	req := &pb.ModerateForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Action: "hide", ExpectedRevision: topic.Revision}
	_, err := c.ModerateForumThread(f.owner, req)
	requireCode(t, err, codes.InvalidArgument)
	req.Reason = "Правила"
	req.Action = "invalid"
	_, err = c.ModerateForumThread(f.owner, req)
	requireCode(t, err, codes.InvalidArgument)
	req.Action = "reopen"
	_, err = c.ModerateForumThread(f.owner, req)
	requireCode(t, err, codes.FailedPrecondition)
	if _, err = f.store.pool.Exec(f.ctx, "ALTER TABLE forum_moderation ADD CONSTRAINT test_reject_hide CHECK(action <> 'hide')"); err != nil {
		t.Fatal(err)
	}
	req.Action = "hide"
	_, err = c.ModerateForumThread(f.owner, req)
	if err == nil {
		t.Fatal("Ожидался отказ аудита")
	}
	detail, err := c.GetForumThread(f.owner, &pb.GetForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id})
	if err != nil || detail.Thread.Hidden || detail.Thread.Revision != topic.Revision {
		t.Fatal(detail, err)
	}
	if _, err = f.store.pool.Exec(f.ctx, "ALTER TABLE forum_moderation DROP CONSTRAINT test_reject_hide"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := c.ModerateForumThread(f.owner, req); results <- err }()
	}
	wg.Wait()
	close(results)
	success, aborted := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if status.Code(err) == codes.Aborted {
			aborted++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || aborted != 1 {
		t.Fatal(success, aborted)
	}
	var n int
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM forum_moderation WHERE thread_id=$1", topic.Id).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestForumModerationUpgradeFrom28(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	section := forumSection(t, f, "upgrade29")
	topic := createTopic(t, c, f.owner, section.Id, "before29")
	reply, err := c.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Сохранить ответ", IdempotencyKey: "before29"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.pool.Exec(f.ctx, "DROP TABLE forum_moderation; ALTER TABLE forum_threads DROP COLUMN hidden; ALTER TABLE forum_replies DROP COLUMN hidden, DROP COLUMN revision; DELETE FROM schema_migrations WHERE version=29"); err != nil {
		t.Fatal(err)
	}
	upgraded, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var hidden bool
	var text string
	var revision int64
	err = upgraded.pool.QueryRow(f.ctx, "SELECT body,hidden,revision FROM forum_replies WHERE id=$1", reply.Reply.Id).Scan(&text, &hidden, &revision)
	if err != nil || text != "Сохранить ответ" || hidden || revision != 1 {
		t.Fatal(text, hidden, revision, err)
	}
}
