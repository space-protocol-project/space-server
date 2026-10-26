package postgres

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func spaceSearchSeed(t *testing.T, f *channelFixture) []*pb.Channel {
	t.Helper()
	chat := f.create(t, "space_chat")
	forum := forumSection(t, f, "space_forum")
	feed := feedSection(t, f, "space_feed")
	if _, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: chat.Id, Text: "СОВЕТ 中文 100%_", IdempotencyKey: "space-message"}); err != nil {
		t.Fatal(err)
	}
	fa := pb.NewForumServiceClient(f.conn)
	topic, err := fa.CreateForumThread(f.owner, &pb.CreateForumThreadRequest{ChannelId: forum.Id, Title: "СОВЕТ 中文 100%_", Text: "Тема", IdempotencyKey: "space-topic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fa.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: forum.Id, ThreadId: topic.Thread.Id, Text: "совет 中文 100%_", IdempotencyKey: "space-reply"}); err != nil {
		t.Fatal(err)
	}
	post := feedCreate(t, f, f.owner, feed.Id, "совет 中文 100%_")
	if _, err = pb.NewFeedServiceClient(f.conn).CommentFeedPost(f.owner, &pb.CommentFeedPostRequest{ChannelId: feed.Id, PostId: post.Id, Text: "совет 中文 100%_", IdempotencyKey: "space-comment"}); err != nil {
		t.Fatal(err)
	}
	return []*pb.Channel{chat, forum, feed}
}

func spaceSearchPage(t *testing.T, f *channelFixture, ctx context.Context, query string) *pb.SearchSpaceResponse {
	t.Helper()
	page, err := pb.NewSpaceSearchServiceClient(f.conn).SearchSpace(ctx, &pb.SearchSpaceRequest{Query: query})
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestSpaceSearchFiveKindsPagesAndUnicode(t *testing.T) {
	f := newChannelFixture(t)
	member, _ := f.enroll(t, true)
	spaceSearchSeed(t, f)
	for _, table := range []string{"forum_threads", "forum_replies", "feed_posts", "feed_comments"} {
		if _, err := f.store.pool.Exec(f.ctx, "UPDATE "+table+" SET created_at=1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.store.pool.Exec(f.ctx, "UPDATE contents SET created_at_unix_ms=1000"); err != nil {
		t.Fatal(err)
	}
	api := pb.NewSpaceSearchServiceClient(f.conn)
	token := ""
	seen := map[string]bool{}
	for kind := 5; kind >= 1; kind-- {
		page, err := api.SearchSpace(member, &pb.SearchSpaceRequest{Query: "совет", Limit: 1, PageToken: token})
		if err != nil || len(page.GetResults()) != 1 {
			t.Fatal(page, err)
		}
		item := page.Results[0]
		if int(item.Kind) != kind || seen[item.Id] || item.ChannelTitle == "" || item.ParentId == "" || item.CreatedAtUnixMs != 1000 {
			t.Fatal(item)
		}
		seen[item.Id] = true
		token = page.NextPageToken
		if (kind == 1) != (token == "") {
			t.Fatal("Неправильная последняя страница", page)
		}
	}
	for _, query := range []string{"СОВЕТ", "中", "中文", "100%_"} {
		if page := spaceSearchPage(t, f, member, query); len(page.Results) != 5 {
			t.Fatal(query, page)
		}
	}
	for _, query := range []string{"' OR true --", "missing"} {
		if page := spaceSearchPage(t, f, member, query); len(page.Results) != 0 || page.NextPageToken != "" {
			t.Fatal(page)
		}
	}
	for _, query := range []string{"", "  ", strings.Repeat("я", 129), "bad\x00text"} {
		_, err := api.SearchSpace(member, &pb.SearchSpaceRequest{Query: query})
		requireCode(t, err, codes.InvalidArgument)
	}
	for _, limit := range []int32{-1, 51} {
		_, err := api.SearchSpace(member, &pb.SearchSpaceRequest{Query: "совет", Limit: limit})
		requireCode(t, err, codes.InvalidArgument)
	}
}

func TestSpaceSearchConnectACLParentsAndCurrentName(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, true)
	sections := spaceSearchSeed(t, f)
	server := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer server.Close()
	api := rpc.NewSpaceSearchServiceClient(server.Client(), server.URL)
	md, _ := metadata.FromOutgoingContext(member)
	request := func(token string) *pb.SearchSpaceResponse {
		req := connect.NewRequest(&pb.SearchSpaceRequest{Query: "совет", Limit: 1, PageToken: token})
		req.Header().Set("Authorization", md.Get("authorization")[0])
		result, err := api.SearchSpace(f.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return result.Msg
	}
	first := request("")
	if len(first.Results) != 1 || first.NextPageToken == "" {
		t.Fatal(first)
	}
	for _, section := range sections {
		f.acl(t, section, principalRule(id, permissions(false, false, false, false)))
	}
	if page := request(first.NextPageToken); len(page.Results) != 0 || page.NextPageToken != "" {
		t.Fatal("ACL после курсора", page)
	}
	for _, section := range sections {
		f.acl(t, section, principalRule(id, permissions(true, true, false, false)))
	}
	if _, err := f.store.pool.Exec(f.ctx, "UPDATE memberships SET nickname='Текущее имя автора' WHERE principal_id=$1", f.ownerID); err != nil {
		t.Fatal(err)
	}
	page := spaceSearchPage(t, f, member, "совет")
	for _, item := range page.Results {
		if item.AuthorName != "Текущее имя автора" {
			t.Fatal(item)
		}
	}
	if _, err := f.store.pool.Exec(f.ctx, "UPDATE forum_threads SET hidden=true"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(f.ctx, "UPDATE feed_posts SET hidden=true"); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []context.Context{member, f.owner} {
		page = spaceSearchPage(t, f, actor, "совет")
		if len(page.Results) != 1 || page.Results[0].Kind != pb.SpaceSearchKind_SPACE_SEARCH_KIND_CHAT_MESSAGE {
			t.Fatal("Скрытый родитель", page)
		}
	}
	message := page.Results[0]
	var revision int64
	if err := f.store.pool.QueryRow(f.ctx, "SELECT revision FROM contents WHERE id=$1", message.Id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.contents.DeleteContent(f.owner, &pb.DeleteContentRequest{ChannelId: sections[0].Id, Id: message.Id, ExpectedRevision: revision}); err != nil {
		t.Fatal(err)
	}
	if page = spaceSearchPage(t, f, member, "совет"); len(page.Results) != 0 {
		t.Fatal("Удалённое сообщение", page)
	}
}

func TestSpaceSearchSessionScopeBanAndCursor(t *testing.T) {
	f := newChannelFixture(t)
	spaceSearchSeed(t, f)
	api := pb.NewSpaceSearchServiceClient(f.conn)
	for _, test := range []struct {
		name, sql string
		code      codes.Code
	}{
		{"scope", "UPDATE device_grants SET scopes=ARRAY['chat.write'] WHERE principal_id=$1", codes.PermissionDenied},
		{"grant", "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", codes.Unauthenticated},
		{"session", "UPDATE auth_sessions SET expires_at=now()-interval '1 second' WHERE grant_id IN(SELECT id FROM device_grants WHERE principal_id=$1)", codes.Unauthenticated},
		{"membership", "DELETE FROM memberships WHERE principal_id=$1", codes.PermissionDenied},
		{"blocked", "UPDATE memberships SET blocked=true WHERE principal_id=$1", codes.PermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			member, id := f.enroll(t, true)
			first, err := api.SearchSpace(member, &pb.SearchSpaceRequest{Query: "совет", Limit: 1})
			if err != nil || first.GetNextPageToken() == "" {
				t.Fatal(first, err)
			}
			if _, err = f.store.pool.Exec(f.ctx, test.sql, id); err != nil {
				t.Fatal(err)
			}
			_, err = api.SearchSpace(member, &pb.SearchSpaceRequest{Query: "совет", PageToken: first.NextPageToken})
			requireCode(t, err, test.code)
		})
	}
	member, _ := f.enroll(t, true)
	other, _ := f.enroll(t, true)
	first, err := api.SearchSpace(member, &pb.SearchSpaceRequest{Query: "совет", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		actor        context.Context
		query, token string
	}{
		{other, "совет", first.NextPageToken}, {member, "другой", first.NextPageToken},
		{member, "совет", "!"}, {member, "совет", strings.Repeat("x", 1025)},
	} {
		_, err = api.SearchSpace(test.actor, &pb.SearchSpaceRequest{Query: test.query, PageToken: test.token})
		requireCode(t, err, codes.InvalidArgument)
	}
	_, err = api.SearchSpace(f.ctx, &pb.SearchSpaceRequest{Query: "совет"})
	requireCode(t, err, codes.Unauthenticated)
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE channels SET archived=true"); err != nil {
		t.Fatal(err)
	}
	if page := spaceSearchPage(t, f, member, "совет"); len(page.Results) != 0 {
		t.Fatal("Архив", page)
	}
}
