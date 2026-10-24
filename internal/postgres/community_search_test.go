package postgres

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func TestCommunitySearchScopePagesAndUnicode(t *testing.T) {
	f := newChannelFixture(t)
	api := pb.NewCommunitySearchServiceClient(f.conn)
	member, _ := f.enroll(t, true)
	other, _ := f.enroll(t, true)
	forum := forumSection(t, f, "search_forum")
	feed := feedSection(t, f, "search_feed")
	fc := pb.NewForumServiceClient(f.conn)
	topic, err := fc.CreateForumThread(f.owner, &pb.CreateForumThreadRequest{ChannelId: forum.Id, Title: "СОВЕТ 100%_", Text: "Русский 中文", IdempotencyKey: "search"})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := fc.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: forum.Id, ThreadId: topic.Thread.Id, Text: "совет из ответа 中文", IdempotencyKey: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	post := feedCreate(t, f, f.owner, feed.Id, "совет 中文")
	fa := pb.NewFeedServiceClient(f.conn)
	comment, err := fa.CommentFeedPost(f.owner, &pb.CommentFeedPostRequest{ChannelId: feed.Id, PostId: post.Id, Text: "СОВЕТ комментарий 中文", IdempotencyKey: "comment"})
	if err != nil {
		t.Fatal(err)
	}
	// Совпадения в другом разделе того же типа не должны попадать в выдачу.
	secondForum := forumSection(t, f, "search_other_forum")
	_, err = fc.CreateForumThread(f.owner, &pb.CreateForumThreadRequest{ChannelId: secondForum.Id, Title: "Совет", Text: "中文", IdempotencyKey: "other"})
	if err != nil {
		t.Fatal(err)
	}
	secondFeed := feedSection(t, f, "search_other_feed")
	feedCreate(t, f, f.owner, secondFeed.Id, "Совет 中文")
	for _, channel := range []string{forum.Id, feed.Id} {
		first, err := api.SearchCommunityContent(member, &pb.SearchCommunityContentRequest{ChannelId: channel, Query: "совет", Limit: 1})
		if err != nil || len(first.GetResults()) != 1 || first.NextPageToken == "" {
			t.Fatal(first, err)
		}
		second, err := api.SearchCommunityContent(member, &pb.SearchCommunityContentRequest{ChannelId: channel, Query: "совет", Limit: 1, PageToken: first.NextPageToken})
		if err != nil || len(second.GetResults()) != 1 || second.Results[0].Id == first.Results[0].Id || second.NextPageToken != "" {
			t.Fatal(second, err)
		}
		for _, ctx := range []context.Context{member, other} {
			query := "другой"
			if ctx == other {
				query = "совет"
			}
			_, err = api.SearchCommunityContent(ctx, &pb.SearchCommunityContentRequest{ChannelId: channel, Query: query, PageToken: first.NextPageToken})
			requireCode(t, err, codes.InvalidArgument)
		}
		for _, query := range []string{"中", "中文", "СОВЕТ"} {
			page, err := api.SearchCommunityContent(member, &pb.SearchCommunityContentRequest{ChannelId: channel, Query: query})
			if err != nil || len(page.GetResults()) != 2 {
				t.Fatal(query, page, err)
			}
		}
	}
	literal, err := api.SearchCommunityContent(member, &pb.SearchCommunityContentRequest{ChannelId: forum.Id, Query: "100%_"})
	if err != nil || len(literal.GetResults()) != 1 || literal.Results[0].Id != topic.Thread.Id {
		t.Fatal(literal, err)
	}
	found, err := api.SearchCommunityContent(member, &pb.SearchCommunityContentRequest{ChannelId: feed.Id, Query: "комментарий"})
	if err != nil || len(found.GetResults()) != 1 || found.Results[0].Id != comment.Comment.Id || found.Results[0].ParentId != post.Id || found.Results[0].Sequence != comment.Comment.Sequence {
		t.Fatal(found, err)
	}
	found, err = api.SearchCommunityContent(member, &pb.SearchCommunityContentRequest{ChannelId: forum.Id, Query: "ответа"})
	if err != nil || len(found.GetResults()) != 1 || found.Results[0].Id != reply.Reply.Id {
		t.Fatal(found, err)
	}
	for _, query := range []string{"", "  ", strings.Repeat("я", 129), "bad\x00text"} {
		_, err = api.SearchCommunityContent(member, &pb.SearchCommunityContentRequest{ChannelId: forum.Id, Query: query})
		requireCode(t, err, codes.InvalidArgument)
	}
	_, err = api.SearchCommunityContent(f.ctx, &pb.SearchCommunityContentRequest{ChannelId: forum.Id, Query: "совет"})
	requireCode(t, err, codes.Unauthenticated)
	chat := f.create(t, "search_chat")
	_, err = api.SearchCommunityContent(member, &pb.SearchCommunityContentRequest{ChannelId: chat.Id, Query: "совет"})
	requireCode(t, err, codes.FailedPrecondition)
}

func TestCommunitySearchConnectACLAndHiddenParents(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, true)
	section := feedSection(t, f, "search_permissions")
	post := feedCreate(t, f, f.owner, section.Id, "Секрет")
	fa := pb.NewFeedServiceClient(f.conn)
	reply, err := fa.CommentFeedPost(f.owner, &pb.CommentFeedPostRequest{ChannelId: section.Id, PostId: post.Id, Text: "Секрет ответ", IdempotencyKey: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer server.Close()
	api := rpc.NewCommunitySearchServiceClient(server.Client(), server.URL)
	md, _ := metadata.FromOutgoingContext(member)
	search := func(token string) (*pb.SearchCommunityContentResponse, error) {
		req := connect.NewRequest(&pb.SearchCommunityContentRequest{ChannelId: section.Id, Query: "секрет", Limit: 1, PageToken: token})
		req.Header().Set("Authorization", md.Get("authorization")[0])
		page, e := api.SearchCommunityContent(f.ctx, req)
		if e != nil {
			return nil, e
		}
		return page.Msg, nil
	}
	page, err := search("")
	if err != nil || len(page.GetResults()) != 1 || page.NextPageToken == "" {
		t.Fatal(page, err)
	}
	token := page.NextPageToken
	f.acl(t, section, roleRule("member", permissions(true, false, false, true)))
	_, err = search(token)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	f.acl(t, section, roleRule("member", permissions(false, false, false, false)))
	_, err = search("")
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
	f.acl(t, section, principalRule(id, permissions(true, true, false, false)))
	hidden, err := fa.ModerateFeedPost(f.owner, &pb.ModerateFeedPostRequest{ChannelId: section.Id, PostId: post.Id, ExpectedRevision: reply.Post.Revision, Hidden: true, Reason: "Скрытие"})
	if err != nil {
		t.Fatal(err)
	}
	page, err = search("")
	if err != nil || len(page.GetResults()) != 0 || page.NextPageToken != "" {
		t.Fatal(page, err)
	}
	_, err = fa.ModerateFeedPost(f.owner, &pb.ModerateFeedPostRequest{ChannelId: section.Id, PostId: post.Id, ExpectedRevision: hidden.Post.Revision, Hidden: false, Reason: "Восстановление"})
	if err != nil {
		t.Fatal(err)
	}
	page, err = search("")
	if err != nil || len(page.GetResults()) != 1 {
		t.Fatal(page, err)
	}
	_, err = fa.ModerateFeedComment(f.owner, &pb.ModerateFeedCommentRequest{ChannelId: section.Id, PostId: post.Id, CommentId: reply.Comment.Id, ExpectedRevision: reply.Comment.Revision, Hidden: true, Reason: "Отдельное скрытие"})
	if err != nil {
		t.Fatal(err)
	}
	page, err = search("")
	if err != nil || len(page.GetResults()) != 1 || page.Results[0].Id != post.Id || page.NextPageToken != "" {
		t.Fatal(page, err)
	}
	_, err = api.SearchCommunityContent(f.ctx, connect.NewRequest(&pb.SearchCommunityContentRequest{ChannelId: section.Id, Query: "секрет"}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal(err)
	}
	// Блокировка участника отзывается и для уже выданного курсора.
	var revision int64
	if err = f.store.pool.QueryRow(f.ctx, "SELECT revision FROM memberships WHERE principal_id=$1", id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	_, err = f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: id, Role: "member", Blocked: true, ExpectedRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = search(token)
	if connect.CodeOf(err) != connect.CodePermissionDenied && connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal(err)
	}
}

func TestCommunitySnippetUnicode(t *testing.T) {
	body := strings.Repeat("я😀", 150) + "ИСКОМОЕ" + strings.Repeat("中", 300)
	snippet := communitySnippet(body, "искомое")
	if len([]rune(snippet)) > 240 || !strings.Contains(snippet, "ИСКОМОЕ") {
		t.Fatal(snippet)
	}
}

func TestCommunitySearchDeviceSessionAndMembership(t *testing.T) {
	f := newChannelFixture(t)
	section := forumSection(t, f, "search_grants")
	createTopic(t, pb.NewForumServiceClient(f.conn), f.owner, section.Id, "secret")
	api := pb.NewCommunitySearchServiceClient(f.conn)
	for _, test := range []struct {
		name, sql string
		code      codes.Code
	}{
		{"scope", "UPDATE device_grants SET scopes=ARRAY['chat.write'] WHERE principal_id=$1", codes.PermissionDenied},
		{"grant", "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", codes.Unauthenticated},
		{"session", "UPDATE auth_sessions SET expires_at=now()-interval '1 second' WHERE grant_id IN(SELECT id FROM device_grants WHERE principal_id=$1)", codes.Unauthenticated},
		{"membership", "DELETE FROM memberships WHERE principal_id=$1", codes.PermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			member, id := f.enroll(t, true)
			req := &pb.SearchCommunityContentRequest{ChannelId: section.Id, Query: "Тема"}
			page, err := api.SearchCommunityContent(member, req)
			if err != nil || len(page.GetResults()) != 1 {
				t.Fatal(page, err)
			}
			if _, err = f.store.pool.Exec(f.ctx, test.sql, id); err != nil {
				t.Fatal(err)
			}
			_, err = api.SearchCommunityContent(member, req)
			requireCode(t, err, test.code)
		})
	}
	if _, err := f.store.pool.Exec(f.ctx, "UPDATE channels SET archived=true WHERE id=$1", section.Id); err != nil {
		t.Fatal(err)
	}
	_, err := api.SearchCommunityContent(f.owner, &pb.SearchCommunityContentRequest{ChannelId: section.Id, Query: "Тема"})
	requireCode(t, err, codes.FailedPrecondition)
}

func TestCommunitySearchForumHiddenAndInvalidCursor(t *testing.T) {
	f := newChannelFixture(t)
	section := forumSection(t, f, "search_hidden_forum")
	fc := pb.NewForumServiceClient(f.conn)
	topic := createTopic(t, fc, f.owner, section.Id, "thread")
	reply, err := fc.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Первое сообщение ответ", IdempotencyKey: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	api := pb.NewCommunitySearchServiceClient(f.conn)
	req := &pb.SearchCommunityContentRequest{ChannelId: section.Id, Query: "Первое сообщение", Limit: 1}
	first, err := api.SearchCommunityContent(f.owner, req)
	if err != nil || first.GetNextPageToken() == "" {
		t.Fatal(first, err)
	}
	req.PageToken = "broken"
	_, err = api.SearchCommunityContent(f.owner, req)
	requireCode(t, err, codes.InvalidArgument)
	req.PageToken = first.NextPageToken
	req.ChannelId = "another"
	_, err = api.SearchCommunityContent(f.owner, req)
	requireCode(t, err, codes.InvalidArgument)
	req.ChannelId = section.Id
	req.PageToken = ""
	req.Limit = 51
	_, err = api.SearchCommunityContent(f.owner, req)
	requireCode(t, err, codes.InvalidArgument)
	req.Limit = 0
	hidden, err := fc.ModerateForumReply(f.owner, &pb.ModerateForumReplyRequest{ChannelId: section.Id, ThreadId: topic.Id, ReplyId: reply.Reply.Id, ExpectedRevision: reply.Reply.Revision, Hidden: true, Reason: "Причина"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := api.SearchCommunityContent(f.owner, req)
	if err != nil || len(page.GetResults()) != 1 || page.Results[0].Id != topic.Id {
		t.Fatal(page, err)
	}
	_, err = fc.ModerateForumThread(f.owner, &pb.ModerateForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, ExpectedRevision: hidden.Thread.Revision, Action: "hide", Reason: "Причина"})
	if err != nil {
		t.Fatal(err)
	}
	page, err = api.SearchCommunityContent(f.owner, req)
	if err != nil || len(page.GetResults()) != 0 {
		t.Fatal(page, err)
	}
}

func TestCommunitySearchUpgradeFrom31(t *testing.T) {
	f := newChannelFixture(t)
	forum := forumSection(t, f, "search_upgrade_forum")
	feed := feedSection(t, f, "search_upgrade_feed")
	fc := pb.NewForumServiceClient(f.conn)
	topic := createTopic(t, fc, f.owner, forum.Id, "legacy")
	_, err := fc.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: forum.Id, ThreadId: topic.Id, Text: "Первое сообщение ответ", IdempotencyKey: "legacy-reply"})
	if err != nil {
		t.Fatal(err)
	}
	post := feedCreate(t, f, f.owner, feed.Id, "Первое сообщение")
	_, err = pb.NewFeedServiceClient(f.conn).CommentFeedPost(f.owner, &pb.CommentFeedPostRequest{ChannelId: feed.Id, PostId: post.Id, Text: "Первое сообщение комментарий", IdempotencyKey: "legacy-comment"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.pool.Exec(f.ctx, `ALTER TABLE forum_threads DROP COLUMN search_text;
 ALTER TABLE forum_replies DROP COLUMN search_text;
 ALTER TABLE feed_posts DROP COLUMN search_text;
 ALTER TABLE feed_comments DROP COLUMN search_text;
 DELETE FROM schema_migrations WHERE version=32;`)
	if err != nil {
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
	for table, rows := range before {
		if rows != after[table] {
			t.Fatalf("Изменены строки %s", table)
		}
	}
	api := pb.NewCommunitySearchServiceClient(f.conn)
	for _, channel := range []string{forum.Id, feed.Id} {
		page, err := api.SearchCommunityContent(f.owner, &pb.SearchCommunityContentRequest{ChannelId: channel, Query: "первое сообщение"})
		if err != nil || len(page.GetResults()) != 2 {
			t.Fatal(page, err)
		}
	}
	reopened, same, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	if same.ServerID != identity.ServerID {
		t.Fatal("Изменилась идентичность после повторного открытия")
	}
}
