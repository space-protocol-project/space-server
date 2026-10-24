package postgres

import (
	"context"
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
)

func feedSection(t *testing.T, f *channelFixture, id string) *pb.Channel {
	t.Helper()
	r, err := f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: id, Title: "Лента", ViewType: "feed"})
	if err != nil {
		t.Fatal(err)
	}
	return r.Channel
}
func feedPolicy(t *testing.T, f *channelFixture, c *pb.Channel, rules ...*pb.FeedAccessRule) {
	t.Helper()
	r, err := pb.NewFeedServiceClient(f.conn).UpdateFeedAccess(f.owner, &pb.UpdateFeedAccessRequest{ChannelId: c.Id, ExpectedRevision: c.Revision, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	c.Revision = r.Revision
}
func feedRole(role string, publish, comment bool) *pb.FeedAccessRule {
	return &pb.FeedAccessRule{Role: role, Permissions: &pb.FeedPermissions{Publish: publish, Comment: comment}}
}
func feedCreate(t *testing.T, f *channelFixture, actor context.Context, channel, key string) *pb.FeedPost {
	t.Helper()
	r, err := pb.NewFeedServiceClient(f.conn).CreateFeedPost(actor, &pb.CreateFeedPostRequest{ChannelId: channel, Text: "Публикация " + key, IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return r.Post
}

func TestFeedIndependentPermissionsReaderAndRevocation(t *testing.T) {
	f := newChannelFixture(t)
	c := feedSection(t, f, "feed_policy")
	api := pb.NewFeedServiceClient(f.conn)
	member, id := f.enroll(t, true)
	req := &pb.CreateFeedPostRequest{ChannelId: c.Id, Text: "Новость", IdempotencyKey: "one"}
	_, err := api.CreateFeedPost(member, req)
	requireCode(t, err, codes.PermissionDenied)
	feedPolicy(t, f, c, feedRole("member", false, true))
	topic := feedCreate(t, f, f.owner, c.Id, "owner")
	_, err = api.CommentFeedPost(member, &pb.CommentFeedPostRequest{ChannelId: c.Id, PostId: topic.Id, Text: "Ответ", IdempotencyKey: "comment"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.CreateFeedPost(member, req)
	requireCode(t, err, codes.PermissionDenied)
	feedPolicy(t, f, c, feedRole("member", true, false))
	made, err := api.CreateFeedPost(member, req)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := api.CreateFeedPost(member, req)
	if err != nil || made.Post.Id != repeated.Post.Id {
		t.Fatal(repeated, err)
	}
	_, err = api.CommentFeedPost(member, &pb.CommentFeedPostRequest{ChannelId: c.Id, PostId: topic.Id, Text: "Новый", IdempotencyKey: "other"})
	requireCode(t, err, codes.PermissionDenied)
	feedPolicy(t, f, c)
	_, err = api.CreateFeedPost(member, req)
	requireCode(t, err, codes.PermissionDenied)
	// Роль reader не получает право публикации даже из явного правила.
	var revision int64
	if err = f.store.pool.QueryRow(f.ctx, "SELECT revision FROM memberships WHERE principal_id=$1", id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err = f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: id, Role: "reader", ExpectedRevision: revision}); err != nil {
		t.Fatal(err)
	}
	feedPolicy(t, f, c, feedRole("reader", true, true))
	_, err = api.CreateFeedPost(member, req)
	requireCode(t, err, codes.PermissionDenied)
	file := forumFile(t, f, member, c.Id, "reader-file")
	_, err = api.CommentFeedPost(member, &pb.CommentFeedPostRequest{ChannelId: c.Id, PostId: topic.Id, IdempotencyKey: "reader-comment", AttachmentIds: []string{file.Id}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.contents.CreateContent(member, &pb.CreateContentRequest{ChannelId: c.Id, Text: "Обход", IdempotencyKey: "bypass"})
	requireCode(t, err, codes.PermissionDenied)
	feedPolicy(t, f, c)
	_, err = pb.NewAssetServiceClient(f.conn).BeginUpload(member, &pb.BeginUploadRequest{ChannelId: c.Id, Name: "later.txt", ByteSize: 1, Sha256: make([]byte, 32), IdempotencyKey: "later"})
	requireCode(t, err, codes.PermissionDenied)
}

func TestFeedPagesReplayHiddenAssetsAndRestore(t *testing.T) {
	f := newChannelFixture(t)
	c := feedSection(t, f, "feed_assets")
	api := pb.NewFeedServiceClient(f.conn)
	member, _ := f.enroll(t, true)
	feedPolicy(t, f, c, feedRole("member", true, true))
	a := feedCreate(t, f, member, c.Id, "a")
	b := feedCreate(t, f, member, c.Id, "b")
	d := feedCreate(t, f, member, c.Id, "d")
	page, err := api.ListFeedPosts(member, &pb.ListFeedPostsRequest{ChannelId: c.Id, Limit: 2})
	if err != nil || len(page.Posts) != 2 || page.Posts[0].Id != d.Id || page.NextBeforeSequence != b.Sequence {
		t.Fatal(page, err)
	}
	feedCreate(t, f, member, c.Id, "new-between-pages")
	next, err := api.ListFeedPosts(member, &pb.ListFeedPostsRequest{ChannelId: c.Id, Limit: 2, BeforeSequence: page.NextBeforeSequence})
	if err != nil || len(next.Posts) != 1 || next.Posts[0].Id != a.Id || next.NextBeforeSequence != 0 {
		t.Fatal(next, err)
	}
	file := forumFile(t, f, member, c.Id, "post-file")
	req := &pb.CreateFeedPostRequest{ChannelId: c.Id, Text: "С файлом", IdempotencyKey: "file", AttachmentIds: []string{file.Id}}
	made, err := api.CreateFeedPost(member, req)
	if err != nil {
		t.Fatal(err)
	}
	assetAPI := pb.NewAssetServiceClient(f.conn)
	if _, err = assetAPI.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: file.Id}); err != nil {
		t.Fatal(err)
	}
	commentFile := forumFile(t, f, member, c.Id, "comment-file")
	comment, err := api.CommentFeedPost(member, &pb.CommentFeedPostRequest{ChannelId: c.Id, PostId: made.Post.Id, IdempotencyKey: "attached-comment", AttachmentIds: []string{commentFile.Id}})
	if err != nil {
		t.Fatal(err)
	}
	hiddenComment, err := api.ModerateFeedComment(f.owner, &pb.ModerateFeedCommentRequest{ChannelId: c.Id, PostId: made.Post.Id, CommentId: comment.Comment.Id, ExpectedRevision: comment.Comment.Revision, Hidden: true, Reason: "Проверка"})
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := api.ModerateFeedPost(f.owner, &pb.ModerateFeedPostRequest{ChannelId: c.Id, PostId: made.Post.Id, ExpectedRevision: hiddenComment.Post.Revision, Hidden: true, Reason: "Проверка"})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := api.GetFeedPost(member, &pb.GetFeedPostRequest{ChannelId: c.Id, PostId: made.Post.Id})
	if err != nil || detail.Post.Text != "" || detail.Post.AuthorId != "" || detail.Post.CommentCount != 0 || len(detail.Comments) != 0 {
		t.Fatal(detail, err)
	}
	replay, err := api.CreateFeedPost(member, req)
	if err != nil || !replay.Post.Hidden || len(replay.Post.Attachments) != 0 || replay.Post.Text != "" {
		t.Fatal(replay, err)
	}
	for _, id := range []string{file.Id, commentFile.Id} {
		_, err = assetAPI.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: id})
		requireCode(t, err, codes.NotFound)
	}
	catalog, err := assetAPI.ListMyAssets(member, &pb.ListMyAssetsRequest{ChannelId: c.Id})
	if err != nil || len(catalog.Assets) != 0 {
		t.Fatal(catalog, err)
	}
	_, err = api.ModerateFeedPost(f.owner, &pb.ModerateFeedPostRequest{ChannelId: c.Id, PostId: made.Post.Id, ExpectedRevision: hidden.Post.Revision, Hidden: false, Reason: "Восстановление"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = assetAPI.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: file.Id}); err != nil {
		t.Fatal(err)
	}
	_, err = assetAPI.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: commentFile.Id})
	requireCode(t, err, codes.NotFound)
	req.Text = "Другой текст"
	_, err = api.CreateFeedPost(member, req)
	requireCode(t, err, codes.AlreadyExists)
}

func TestFeedReportsDecisionsAndConcurrentComments(t *testing.T) {
	f := newChannelFixture(t)
	c := feedSection(t, f, "feed_reports")
	api := pb.NewFeedServiceClient(f.conn)
	member, _ := f.enroll(t, true)
	feedPolicy(t, f, c, feedRole("member", false, true))
	post := feedCreate(t, f, f.owner, c.Id, "post")
	var group sync.WaitGroup
	errors := make(chan error, 2)
	for _, key := range []string{"one", "two"} {
		group.Add(1)
		go func(key string) {
			defer group.Done()
			_, err := api.CommentFeedPost(member, &pb.CommentFeedPostRequest{ChannelId: c.Id, PostId: post.Id, Text: key, IdempotencyKey: key})
			errors <- err
		}(key)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	detail, err := api.GetFeedPost(member, &pb.GetFeedPostRequest{ChannelId: c.Id, PostId: post.Id})
	if err != nil || detail.Post.CommentCount != 2 || detail.Comments[0].Sequence != 1 || detail.Comments[1].Sequence != 2 {
		t.Fatal(detail, err)
	}
	report := &pb.ReportFeedContentRequest{ChannelId: c.Id, PostId: post.Id, Reason: "Нарушение"}
	for range 2 {
		if _, err = api.ReportFeedContent(member, report); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := api.ListFeedReports(f.owner, &pb.ListFeedReportsRequest{ChannelId: c.Id})
	if err != nil || len(pending.Reports) != 1 {
		t.Fatal(pending, err)
	}
	_, err = api.ListFeedReports(member, &pb.ListFeedReportsRequest{ChannelId: c.Id})
	requireCode(t, err, codes.PermissionDenied)
	decision := &pb.ResolveFeedReportRequest{ChannelId: c.Id, ReportId: pending.Reports[0].Id, Decision: "hide", Reason: "Подтверждено", ExpectedRevision: detail.Post.Revision}
	if _, err = api.ResolveFeedReport(f.owner, decision); err != nil {
		t.Fatal(err)
	}
	_, err = api.ResolveFeedReport(f.owner, decision)
	requireCode(t, err, codes.Aborted)
	log, err := api.ListFeedModeration(f.owner, &pb.ListFeedModerationRequest{ChannelId: c.Id, PostId: post.Id})
	if err != nil || len(log.Events) != 2 || log.Events[1].ReportId != decision.ReportId {
		t.Fatal(log, err)
	}
	_, err = api.CommentFeedPost(member, &pb.CommentFeedPostRequest{ChannelId: c.Id, PostId: post.Id, Text: "После скрытия", IdempotencyKey: "after"})
	requireCode(t, err, codes.FailedPrecondition)
}

func TestFeedConnectAndPolicyConflicts(t *testing.T) {
	f := newChannelFixture(t)
	c := feedSection(t, f, "feed_connect")
	server := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer server.Close()
	api := rpc.NewFeedServiceClient(server.Client(), server.URL)
	md, _ := metadata.FromOutgoingContext(f.owner)
	req := connect.NewRequest(&pb.CreateFeedPostRequest{ChannelId: c.Id, Text: "Connect", IdempotencyKey: "connect"})
	req.Header().Set("Authorization", md.Get("authorization")[0])
	result, err := api.CreateFeedPost(f.ctx, req)
	if err != nil || result.Msg.Post.Text != "Connect" {
		t.Fatal(result, err)
	}
	grpcAPI := pb.NewFeedServiceClient(f.conn)
	_, err = grpcAPI.UpdateFeedAccess(f.owner, &pb.UpdateFeedAccessRequest{ChannelId: c.Id, ExpectedRevision: c.Revision + 1})
	requireCode(t, err, codes.Aborted)
	_, err = grpcAPI.UpdateFeedAccess(f.owner, &pb.UpdateFeedAccessRequest{ChannelId: c.Id, ExpectedRevision: c.Revision, Rules: []*pb.FeedAccessRule{feedRole("member", true, false), feedRole("member", false, true)}})
	requireCode(t, err, codes.InvalidArgument)
	for _, kind := range []string{"forum", "chat", "voice_room"} {
		other, err := f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: "wrong_" + kind, Title: kind, ViewType: kind})
		if err != nil {
			t.Fatal(err)
		}
		_, err = grpcAPI.CreateFeedPost(f.owner, &pb.CreateFeedPostRequest{ChannelId: other.Channel.Id, Text: "Обход", IdempotencyKey: "wrong"})
		requireCode(t, err, codes.FailedPrecondition)
	}
}

func TestFeedUpgradeFrom30PreservesIdentityAndData(t *testing.T) {
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
	for i := 12; i < 30; i++ {
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
	if identity.ServerID != original.ServerID {
		t.Fatal("Изменилась идентичность")
	}
	after := legacySnapshot(t, ctx, pool)
	for table, value := range before {
		if value != after[table] {
			t.Fatalf("Изменились данные %s", table)
		}
	}
	var version, capacity int
	if err = pool.QueryRow(ctx, "SELECT (SELECT max(version) FROM schema_migrations),voice_capacity FROM channels WHERE id='voice32'").Scan(&version, &capacity); err != nil {
		t.Fatal(err)
	}
	if version != 31 || capacity != 32 {
		t.Fatal(version, capacity)
	}
	store.Close()
	reopened, same, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if same.ServerID != identity.ServerID {
		t.Fatal("Перезапуск сменил идентичность")
	}
}

func TestFeedIndividualRuleScopeArchiveAndAtomicAudit(t *testing.T) {
	f := newChannelFixture(t)
	c := feedSection(t, f, "feed_guards")
	api := pb.NewFeedServiceClient(f.conn)
	member, id := f.enroll(t, true)
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)), principalRule(id, permissions(true, true, true, true)))
	// Manage не предоставляет publish: индивидуальное правило полностью заменяет роль.
	feedPolicy(t, f, c, feedRole("member", true, true), &pb.FeedAccessRule{PrincipalId: id, Permissions: &pb.FeedPermissions{Comment: true}})
	_, err := api.CreateFeedPost(member, &pb.CreateFeedPostRequest{ChannelId: c.Id, Text: "Обход", IdempotencyKey: "bypass"})
	requireCode(t, err, codes.PermissionDenied)
	topic := feedCreate(t, f, f.owner, c.Id, "owner")
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET scopes=ARRAY['chat.read','space.manage'] WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	_, err = api.CommentFeedPost(member, &pb.CommentFeedPostRequest{ChannelId: c.Id, PostId: topic.Id, Text: "Без scope", IdempotencyKey: "scope"})
	requireCode(t, err, codes.PermissionDenied)
	if _, err = f.store.pool.Exec(f.ctx, `CREATE FUNCTION fail_feed_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER fail_feed_audit BEFORE INSERT ON feed_moderation FOR EACH ROW EXECUTE FUNCTION fail_feed_audit()`); err != nil {
		t.Fatal(err)
	}
	_, err = api.ModerateFeedPost(f.owner, &pb.ModerateFeedPostRequest{ChannelId: c.Id, PostId: topic.Id, Hidden: true, ExpectedRevision: topic.Revision, Reason: "Проверка отката"})
	requireCode(t, err, codes.Unavailable)
	fresh, err := api.GetFeedPost(f.owner, &pb.GetFeedPostRequest{ChannelId: c.Id, PostId: topic.Id})
	if err != nil || fresh.Post.Hidden || fresh.Post.Revision != topic.Revision {
		t.Fatal(fresh, err)
	}
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE channels SET archived=true WHERE id=$1", c.Id); err != nil {
		t.Fatal(err)
	}
	_, err = api.CreateFeedPost(f.owner, &pb.CreateFeedPostRequest{ChannelId: c.Id, Text: "Архив", IdempotencyKey: "archive"})
	requireCode(t, err, codes.FailedPrecondition)
	page, err := api.ListFeedPosts(f.owner, &pb.ListFeedPostsRequest{ChannelId: c.Id})
	if err != nil || page.Permissions.Publish || page.Permissions.Comment {
		t.Fatal(page, err)
	}
}
