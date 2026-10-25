package postgres

import (
	"connectrpc.com/connect"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// При имитации старой схемы удаляем зависимые объекты новой миграции явно.
const dropCommunityMentionsSchema = `DROP VIEW community_mention_content; DROP TABLE community_mentions;
 ALTER TABLE forum_threads DROP COLUMN mentions; ALTER TABLE forum_replies DROP COLUMN mentions;
 ALTER TABLE feed_posts DROP COLUMN mentions; ALTER TABLE feed_comments DROP COLUMN mentions;
 DELETE FROM schema_migrations WHERE version=34;`

func mentionRecipient(t *testing.T, f *channelFixture, name string) (context.Context, *pb.CommunityMention) {
	t.Helper()
	ctx, id := f.enroll(t, true)
	if _, err := f.store.pool.Exec(f.ctx, "UPDATE memberships SET nickname=$2 WHERE principal_id=$1", id, name); err != nil {
		t.Fatal(err)
	}
	return ctx, &pb.CommunityMention{PrincipalId: id, DisplayName: name, Length: int32(len(utf16.Encode([]rune("@" + name))))}
}

func TestCommunityMentionsForumInboxAndIndependentAck(t *testing.T) {
	f := newChannelFixture(t)
	root, key, _ := ed25519.GenerateKey(rand.Reader)
	recipient, id := forumDevice(t, f, root, key, true)
	second, _ := forumDevice(t, f, root, key, true)
	if _, err := f.store.pool.Exec(f.ctx, "UPDATE memberships SET nickname=$2 WHERE principal_id=$1", id, "Маша 🚀"); err != nil {
		t.Fatal(err)
	}
	m := &pb.CommunityMention{PrincipalId: id, DisplayName: "Маша 🚀", Length: 8}
	other, _ := f.enroll(t, true)
	section := forumSection(t, f, "mentions_forum")
	api := pb.NewCommunityMentionServiceClient(f.conn)
	candidates, err := api.ListMentionCandidates(f.owner, &pb.ListMentionCandidatesRequest{ChannelId: section.Id, Query: "МАША"})
	if err != nil || len(candidates.GetCandidates()) != 1 || candidates.Candidates[0].PrincipalId != m.PrincipalId {
		t.Fatal(candidates, err)
	}
	fc := pb.NewForumServiceClient(f.conn)
	topic := createTopic(t, fc, f.owner, section.Id, "topic")
	_, err = fc.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Пропущенный ответ", IdempotencyKey: "early"})
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "@" + m.DisplayName, IdempotencyKey: "mention", Mentions: []*pb.CommunityMention{m}}
	reply, err := fc.ReplyForumThread(f.owner, req)
	if err != nil || len(reply.GetReply().GetMentions()) != 1 {
		t.Fatal(reply, err)
	}
	overview := pb.NewCommunityOverviewServiceClient(f.conn)
	page, err := overview.GetCommunityOverview(recipient, &pb.GetCommunityOverviewRequest{ChannelId: section.Id})
	if err != nil || len(page.GetMentions()) != 1 || page.Mentions[0].Content.Id != reply.Reply.Id || page.Mentions[0].Content.Sequence != 2 {
		t.Fatal(page, err)
	}
	ack := &pb.MarkCommunityMentionsReadRequest{ChannelId: section.Id, Contents: []*pb.CommunityContentReference{{Kind: pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FORUM_REPLY, Id: reply.Reply.Id}}}
	if _, err = api.MarkCommunityMentionsRead(other, ack); err != nil {
		t.Fatal(err)
	}
	page, err = overview.GetCommunityOverview(recipient, &pb.GetCommunityOverviewRequest{ChannelId: section.Id})
	if err != nil || len(page.GetMentions()) != 1 {
		t.Fatal(page, err)
	}
	for range 2 {
		if _, err = api.MarkCommunityMentionsRead(second, ack); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = fc.ReplyForumThread(f.owner, req); err != nil {
		t.Fatal(err)
	}
	page, err = overview.GetCommunityOverview(recipient, &pb.GetCommunityOverviewRequest{ChannelId: section.Id})
	if err != nil || len(page.GetMentions()) != 0 || page.Recent[0].UnreadCount != 3 {
		t.Fatal(page, err)
	}
	reopened, identity, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var stored bool
	if err = reopened.pool.QueryRow(f.ctx, "SELECT bool_and(read_at IS NOT NULL) FROM community_mentions WHERE recipient_id=$1", m.PrincipalId).Scan(&stored); err != nil || !stored || identity.ServerID != f.identity.ServerID {
		t.Fatal(stored, err)
	}
	// Повтор принятой операции не зависит от последующего отзыва прав адресата.
	f.acl(t, section, principalRule(m.PrincipalId, permissions(false, false, false, false)))
	if _, err = fc.ReplyForumThread(f.owner, req); err != nil {
		t.Fatal(err)
	}
	req.IdempotencyKey = "new"
	_, err = fc.ReplyForumThread(f.owner, req)
	requireCode(t, err, codes.InvalidArgument)
	_, err = api.MarkCommunityMentionsRead(recipient, ack)
	requireCode(t, err, codes.NotFound)
}

func TestCommunityMentionsValidationAndCandidates(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "Имя%_")
	section := forumSection(t, f, "mentions_validation")
	api := pb.NewCommunityMentionServiceClient(f.conn)
	for _, q := range []string{"%_", "ИМЯ"} {
		page, err := api.ListMentionCandidates(f.owner, &pb.ListMentionCandidatesRequest{ChannelId: section.Id, Query: q})
		if err != nil || len(page.GetCandidates()) != 1 {
			t.Fatal(page, err)
		}
	}
	page, err := api.ListMentionCandidates(recipient, &pb.ListMentionCandidatesRequest{ChannelId: section.Id, Query: "Имя"})
	if err != nil || len(page.GetCandidates()) != 0 {
		t.Fatal(page, err)
	}
	fc := pb.NewForumServiceClient(f.conn)
	body := "😀 @" + m.DisplayName
	m.Start = 3
	for i, bad := range []*pb.CommunityMention{
		{PrincipalId: m.PrincipalId, DisplayName: m.DisplayName, Start: 2, Length: m.Length},
		{PrincipalId: m.PrincipalId, DisplayName: "Подмена", Start: 3, Length: m.Length},
		{PrincipalId: "missing", DisplayName: m.DisplayName, Start: 3, Length: m.Length},
		{PrincipalId: m.PrincipalId, DisplayName: m.DisplayName, Start: 1, Length: 999},
	} {
		_, err = fc.CreateForumThread(f.owner, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Тема", Text: body, IdempotencyKey: fmt.Sprint(i), Mentions: []*pb.CommunityMention{bad}})
		requireCode(t, err, codes.InvalidArgument)
	}
	_, err = fc.CreateForumThread(f.owner, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Тема", Text: body, IdempotencyKey: "overlap", Mentions: []*pb.CommunityMention{m, m}})
	requireCode(t, err, codes.InvalidArgument)
	good, err := fc.CreateForumThread(f.owner, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Тема", Text: body, IdempotencyKey: "good", Mentions: []*pb.CommunityMention{m}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fc.CloseForumThread(f.owner, &pb.CloseForumThreadRequest{ChannelId: section.Id, ThreadId: good.Thread.Id, ExpectedRevision: good.Thread.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.ListMentionCandidates(f.owner, &pb.ListMentionCandidatesRequest{ChannelId: section.Id, ParentId: good.Thread.Id})
	requireCode(t, err, codes.FailedPrecondition)
	f.acl(t, section, principalRule(m.PrincipalId, permissions(true, true, false, false)))
	_, err = api.ListMentionCandidates(recipient, &pb.ListMentionCandidatesRequest{ChannelId: section.Id})
	requireCode(t, err, codes.PermissionDenied)
	// Доступ адресата не зависит от write scope его устройства.
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET scopes=ARRAY['chat.read'] WHERE principal_id=$1", m.PrincipalId); err != nil {
		t.Fatal(err)
	}
	m.Start = 0
	_, err = fc.CreateForumThread(f.owner, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Ещё", Text: "@" + m.DisplayName, IdempotencyKey: "read-only-target", Mentions: []*pb.CommunityMention{m}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE memberships SET blocked=true WHERE principal_id=$1", m.PrincipalId); err != nil {
		t.Fatal(err)
	}
	page, err = api.ListMentionCandidates(f.owner, &pb.ListMentionCandidatesRequest{ChannelId: section.Id, Query: "Имя"})
	if err != nil || len(page.GetCandidates()) != 0 {
		t.Fatal(page, err)
	}
}

func TestCommunityMentionsFeedRepliesModerationAndAck(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "Читатель")
	section := feedSection(t, f, "mentions_feed")
	feedPolicy(t, f, section, feedRole("member", true, true))
	post := feedCreate(t, f, recipient, section.Id, "mine")
	fa := pb.NewFeedServiceClient(f.conn)
	reply, err := fa.CommentFeedPost(f.owner, &pb.CommentFeedPostRequest{ChannelId: section.Id, PostId: post.Id, Text: "@" + m.DisplayName, Mentions: []*pb.CommunityMention{m}, IdempotencyKey: "comment"})
	if err != nil {
		t.Fatal(err)
	}
	overview := pb.NewCommunityOverviewServiceClient(f.conn)
	read := func() *pb.GetCommunityOverviewResponse {
		t.Helper()
		p, e := overview.GetCommunityOverview(recipient, &pb.GetCommunityOverviewRequest{ChannelId: section.Id})
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	page := read()
	if len(page.Mentions) != 1 || len(page.RepliesToYou) != 1 || page.RepliesToYou[0].FirstUnreadReplySequence != 1 {
		t.Fatal(page)
	}
	hidden, err := fa.ModerateFeedComment(f.owner, &pb.ModerateFeedCommentRequest{ChannelId: section.Id, PostId: post.Id, CommentId: reply.Comment.Id, ExpectedRevision: reply.Comment.Revision, Hidden: true, Reason: "Проверка"})
	if err != nil {
		t.Fatal(err)
	}
	page = read()
	if len(page.Mentions) != 0 || len(page.RepliesToYou) != 0 {
		t.Fatal(page)
	}
	detail, err := fa.GetFeedPost(recipient, &pb.GetFeedPostRequest{ChannelId: section.Id, PostId: post.Id})
	if err != nil || len(detail.Comments[0].Mentions) != 0 || detail.Comments[0].Text != "" {
		t.Fatal(detail, err)
	}
	_, err = fa.ModerateFeedComment(f.owner, &pb.ModerateFeedCommentRequest{ChannelId: section.Id, PostId: post.Id, CommentId: reply.Comment.Id, ExpectedRevision: hidden.Comment.Revision, Hidden: false, Reason: "Вернуть"})
	if err != nil {
		t.Fatal(err)
	}
	page = read()
	if len(page.Mentions) != 1 {
		t.Fatal(page)
	}
	api := pb.NewCommunityMentionServiceClient(f.conn)
	_, err = api.MarkCommunityMentionsRead(recipient, &pb.MarkCommunityMentionsReadRequest{ChannelId: section.Id, Contents: []*pb.CommunityContentReference{{Kind: pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FEED_COMMENT, Id: reply.Comment.Id}, {Kind: pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FEED_POST, Id: "missing"}}})
	requireCode(t, err, codes.NotFound)
	if len(read().Mentions) != 1 {
		t.Fatal("Частичная отметка не откатилась")
	}
	detail, err = fa.GetFeedPost(recipient, &pb.GetFeedPostRequest{ChannelId: section.Id, PostId: post.Id})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fa.ModerateFeedPost(f.owner, &pb.ModerateFeedPostRequest{ChannelId: section.Id, PostId: post.Id, ExpectedRevision: detail.Post.Revision, Hidden: true, Reason: "Родитель скрыт"})
	if err != nil {
		t.Fatal(err)
	}
	if len(read().Mentions) != 0 {
		t.Fatal("Скрытый родитель попал в сводку")
	}
}

func TestCommunityMentionsBudgetConcurrentAndAtomic(t *testing.T) {
	f := newChannelFixture(t)
	_, m := mentionRecipient(t, f, "Адресат")
	section := forumSection(t, f, "mentions_budget")
	fc := pb.NewForumServiceClient(f.conn)
	first, err := fc.CreateForumThread(f.owner, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Начало", Text: "@" + m.DisplayName, IdempotencyKey: "first", Mentions: []*pb.CommunityMention{m}})
	if err != nil {
		t.Fatal(err)
	}
	// Оставляем одно место в бюджете, не создавая сотню сетевых запросов.
	_, err = f.store.pool.Exec(f.ctx, `INSERT INTO forum_threads(id,channel_id,author_id,author_name,title,body,sequence,idempotency_key) SELECT 'budget_'||n,$1,$2,'','Тема','Текст',n+100,'budget_'||n FROM generate_series(1,98) n`, section.Id, first.Thread.AuthorId)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.pool.Exec(f.ctx, `INSERT INTO community_mentions(channel_id,author_id,recipient_id,forum_thread_id) SELECT $1,$2,$3,'budget_'||n FROM generate_series(1,98) n`, section.Id, first.Thread.AuthorId, m.PrincipalId)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := fc.CreateForumThread(f.owner, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Гонка", Text: "@" + m.DisplayName, IdempotencyKey: fmt.Sprint("race", i), Mentions: []*pb.CommunityMention{m}})
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	successes, limited := 0, 0
	for e := range results {
		if e == nil {
			successes++
		} else if status.Code(e) == codes.ResourceExhausted {
			limited++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || limited != 1 {
		t.Fatal(successes, limited)
	}
	var threads, alerts int
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM forum_threads WHERE channel_id=$1", section.Id).Scan(&threads); err != nil {
		t.Fatal(err)
	}
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM community_mentions WHERE channel_id=$1", section.Id).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if threads != 100 || alerts != 100 {
		t.Fatal(threads, alerts)
	}
}

func TestCommunityMentionNameAndUTF16(t *testing.T) {
	if mentionName("  Имя\n\u202e  Фамилия  ", "id") != "Имя Фамилия" || mentionName(strings.Repeat("я", 65), "id") != "id" {
		t.Fatal("Небезопасное имя")
	}
	units := utf16.Encode([]rune("😀 @🚀"))
	if !mentionSpan(units, &pb.CommunityMention{Start: 3, Length: 3, DisplayName: "🚀"}, 0) || mentionSpan(units, &pb.CommunityMention{Start: 1, Length: 2, DisplayName: "x"}, 0) {
		t.Fatal("Смещения UTF-16")
	}
}

func TestCommunityMentionsConnectAndRevocation(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "Connect")
	section := feedSection(t, f, "mentions_connect")
	feedPolicy(t, f, section, feedRole("member", false, true))
	api := pb.NewCommunityMentionServiceClient(f.conn)
	// Подбор для новой публикации требует publish, для комментария — comment.
	_, err := api.ListMentionCandidates(recipient, &pb.ListMentionCandidatesRequest{ChannelId: section.Id})
	requireCode(t, err, codes.PermissionDenied)
	post := feedCreate(t, f, f.owner, section.Id, "post")
	page, err := api.ListMentionCandidates(recipient, &pb.ListMentionCandidatesRequest{ChannelId: section.Id, ParentId: post.Id})
	if err != nil || len(page.GetCandidates()) == 0 {
		t.Fatal(page, err)
	}
	server := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer server.Close()
	client := rpc.NewCommunityMentionServiceClient(server.Client(), server.URL)
	md, _ := metadata.FromOutgoingContext(recipient)
	request := func() *connect.Request[pb.MarkCommunityMentionsReadRequest] {
		req := connect.NewRequest(&pb.MarkCommunityMentionsReadRequest{ChannelId: section.Id, Contents: []*pb.CommunityContentReference{{Kind: pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FEED_POST, Id: post.Id}}})
		req.Header().Set("Authorization", md.Get("authorization")[0])
		return req
	}
	if _, err = client.MarkCommunityMentionsRead(f.ctx, request()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{
		"UPDATE device_grants SET scopes=ARRAY['chat.write'] WHERE principal_id=$1",
		"UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1",
	} {
		if _, err = f.store.pool.Exec(f.ctx, change, m.PrincipalId); err != nil {
			t.Fatal(err)
		}
		_, err = client.MarkCommunityMentionsRead(f.ctx, request())
		if connect.CodeOf(err) != connect.CodePermissionDenied && connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatal(err)
		}
	}
}

func TestCommunityMentionsUpgradeFrom33(t *testing.T) {
	f := newChannelFixture(t)
	member, _ := f.enroll(t, true)
	section := feedSection(t, f, "mentions_upgrade")
	post := feedCreate(t, f, f.owner, section.Id, "legacy")
	_, err := pb.NewFeedServiceClient(f.conn).MarkFeedPostRead(member, &pb.MarkFeedPostReadRequest{ChannelId: section.Id, PostId: post.Id})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var rows string
		if e := f.store.pool.QueryRow(f.ctx, `SELECT jsonb_build_object('post',(SELECT to_jsonb(p)-'mentions' FROM feed_posts p WHERE id=$1),'read',(SELECT jsonb_agg(to_jsonb(s)) FROM feed_read_state s WHERE post_id=$1))::text`, post.Id).Scan(&rows); e != nil {
			t.Fatal(e)
		}
		return rows
	}
	before := snapshot()
	_, err = f.store.pool.Exec(f.ctx, dropCommunityMentionsSchema)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, identity, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if identity.ServerID != f.identity.ServerID || snapshot() != before {
		t.Fatal("Миграция изменила данные")
	}
	response, err := pb.NewFeedServiceClient(f.conn).GetFeedPost(member, &pb.GetFeedPostRequest{ChannelId: section.Id, PostId: post.Id})
	if err != nil || !response.Post.Seen || len(response.Post.Mentions) != 0 {
		t.Fatal(response, err)
	}
	reopened, same, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	if identity.ServerID != same.ServerID {
		t.Fatal("Изменился server ID")
	}
}

func TestCommunityMentionsLimitsDedupAndSelf(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "A")
	section := forumSection(t, f, "mentions_limits")
	fc := pb.NewForumServiceClient(f.conn)
	mentions := []*pb.CommunityMention{}
	for i := range 10 {
		mentions = append(mentions, &pb.CommunityMention{PrincipalId: m.PrincipalId, DisplayName: m.DisplayName, Start: int32(i * 3), Length: 2})
	}
	req := &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Повторы", Text: strings.TrimSpace(strings.Repeat("@A ", 10)), IdempotencyKey: "ten", Mentions: mentions}
	if _, err := fc.CreateForumThread(f.owner, req); err != nil {
		t.Fatal(err)
	}
	req.IdempotencyKey = "eleven"
	req.Text += " @A"
	req.Mentions = append(req.Mentions, &pb.CommunityMention{PrincipalId: m.PrincipalId, DisplayName: "A", Start: 30, Length: 2})
	_, err := fc.CreateForumThread(f.owner, req)
	requireCode(t, err, codes.InvalidArgument)
	_, err = fc.CreateForumThread(recipient, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Себе", Text: "@A", IdempotencyKey: "self", Mentions: []*pb.CommunityMention{m}})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM community_mentions WHERE channel_id=$1", section.Id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("Повтор адресата или собственное упоминание создали входящие", count)
	}
}
