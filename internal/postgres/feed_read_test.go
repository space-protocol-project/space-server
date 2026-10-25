package postgres

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func feedReadPost(t *testing.T, f *channelFixture, actor context.Context, post *pb.FeedPost) *pb.FeedPost {
	t.Helper()
	out, err := pb.NewFeedServiceClient(f.conn).GetFeedPost(actor, &pb.GetFeedPostRequest{ChannelId: post.ChannelId, PostId: post.Id})
	if err != nil {
		t.Fatal(err)
	}
	return out.Post
}

func feedReadComment(t *testing.T, f *channelFixture, post *pb.FeedPost, key string) *pb.FeedComment {
	t.Helper()
	out, err := pb.NewFeedServiceClient(f.conn).CommentFeedPost(f.owner, &pb.CommentFeedPostRequest{ChannelId: post.ChannelId, PostId: post.Id, Text: key, IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return out.Comment
}

func TestFeedReadAcrossDevicesAndMonotonicUpdates(t *testing.T) {
	f := newChannelFixture(t)
	section := feedSection(t, f, "feed_read")
	post := feedCreate(t, f, f.owner, section.Id, "post")
	feedReadComment(t, f, post, "one")
	feedReadComment(t, f, post, "two")
	root, key, _ := ed25519.GenerateKey(rand.Reader)
	one, id := forumDevice(t, f, root, key, false)
	two, _ := forumDevice(t, f, root, key, false)
	other, _ := f.enroll(t, false)
	api := pb.NewFeedServiceClient(f.conn)
	initial := feedReadPost(t, f, one, post)
	if !initial.ReadStateSupported || initial.Seen || initial.UnreadCount != 3 {
		t.Fatal(initial)
	}
	if feedReadPost(t, f, two, post).Seen {
		t.Fatal("GET подтвердил чтение")
	}
	mark := &pb.MarkFeedPostReadRequest{ChannelId: section.Id, PostId: post.Id}
	seen, err := api.MarkFeedPostRead(one, mark)
	if err != nil || !seen.GetPost().GetSeen() || seen.Post.UnreadCount != 2 {
		t.Fatal(seen, err)
	}
	if feedReadPost(t, f, other, post).Seen {
		t.Fatal("Раскрыта отметка другой личности")
	}
	for _, invalid := range []int64{-1, 3} {
		_, err = api.MarkFeedPostRead(one, &pb.MarkFeedPostReadRequest{ChannelId: section.Id, PostId: post.Id, ThroughSequence: invalid})
		requireCode(t, err, codes.InvalidArgument)
	}
	var wg sync.WaitGroup
	for _, sequence := range []int64{0, 2, 1, 0, 2} {
		wg.Add(1)
		go func(sequence int64) {
			defer wg.Done()
			_, err := api.MarkFeedPostRead(two, &pb.MarkFeedPostReadRequest{ChannelId: section.Id, PostId: post.Id, ThroughSequence: sequence})
			if err != nil {
				t.Error(err)
			}
		}(sequence)
	}
	wg.Wait()
	current := feedReadPost(t, f, one, post)
	if current.ReadSequence != 2 || current.UnreadCount != 0 || current.Revision != post.Revision+2 {
		t.Fatal(current)
	}
	reopened, identity, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if identity.ServerID != f.identity.ServerID {
		t.Fatal("Изменилась идентичность")
	}
	var sequence int64
	if err = reopened.pool.QueryRow(f.ctx, "SELECT read_sequence FROM feed_read_state WHERE post_id=$1 AND principal_id=$2", post.Id, id).Scan(&sequence); err != nil || sequence != 2 {
		t.Fatal(sequence, err)
	}
	feedReadComment(t, f, post, "future")
	if feedReadPost(t, f, two, post).UnreadCount != 1 {
		t.Fatal("Новый комментарий отмечен заранее")
	}
	if _, err = f.channels.DeleteChannel(f.owner, &pb.DeleteChannelRequest{ChannelId: section.Id, ExpectedRevision: section.Revision}); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM feed_read_state WHERE post_id=$1", post.Id).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal(remaining, err)
	}
}

func TestFeedReadConnectVisibilityAndOverview(t *testing.T) {
	f := newChannelFixture(t)
	member, _ := f.enroll(t, true)
	section := feedSection(t, f, "feed_read_visible")
	post := feedCreate(t, f, f.owner, section.Id, "post")
	comment := feedReadComment(t, f, post, "one")
	api := pb.NewFeedServiceClient(f.conn)
	server := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer server.Close()
	client := rpc.NewFeedServiceClient(server.Client(), server.URL)
	md, _ := metadata.FromOutgoingContext(member)
	mark := func(sequence int64) (*pb.FeedPost, error) {
		req := connect.NewRequest(&pb.MarkFeedPostReadRequest{ChannelId: section.Id, PostId: post.Id, ThroughSequence: sequence})
		req.Header().Set("Authorization", md.Get("authorization")[0])
		out, err := client.MarkFeedPostRead(f.ctx, req)
		if err != nil {
			return nil, err
		}
		return out.Msg.Post, nil
	}
	overview := func() int64 {
		out, err := pb.NewCommunityOverviewServiceClient(f.conn).GetCommunityOverview(member, &pb.GetCommunityOverviewRequest{ChannelId: section.Id})
		if err != nil || len(out.GetRecent()) != 1 || !out.Recent[0].ReadStateSupported {
			t.Fatal(out, err)
		}
		return out.Recent[0].UnreadCount
	}
	if overview() != 2 {
		t.Fatal("Неверный счётчик сводки")
	}
	read, err := mark(1)
	if err != nil || read.UnreadCount != 0 || overview() != 0 {
		t.Fatal(read, err)
	}
	hidden, err := api.ModerateFeedComment(f.owner, &pb.ModerateFeedCommentRequest{ChannelId: section.Id, PostId: post.Id, CommentId: comment.Id, ExpectedRevision: comment.Revision, Hidden: true, Reason: "Причина"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mark(1); err != nil {
		t.Fatal("Повтор сохранённого курсора после скрытия", err)
	}
	stranger, _ := f.enroll(t, false)
	if feedReadPost(t, f, stranger, post).UnreadCount != 1 {
		t.Fatal("Скрытый комментарий вошёл в счётчик")
	}
	_, err = api.MarkFeedPostRead(stranger, &pb.MarkFeedPostReadRequest{ChannelId: section.Id, PostId: post.Id, ThroughSequence: 1})
	requireCode(t, err, codes.InvalidArgument)
	detail := feedReadPost(t, f, f.owner, post)
	_, err = api.ModerateFeedPost(f.owner, &pb.ModerateFeedPostRequest{ChannelId: section.Id, PostId: post.Id, ExpectedRevision: detail.Revision, Hidden: true, Reason: "Причина"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mark(1); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
	redacted := feedReadPost(t, f, member, post)
	if redacted.ReadStateSupported || redacted.Seen || redacted.ReadSequence != 0 || redacted.UnreadCount != 0 {
		t.Fatal(redacted)
	}
	// Нельзя заранее отметить только скрытый комментарий без своей старой отметки.
	_, err = api.MarkFeedPostRead(stranger, &pb.MarkFeedPostReadRequest{ChannelId: section.Id, PostId: post.Id, ThroughSequence: hidden.Comment.Sequence})
	requireCode(t, err, codes.NotFound)
}

func TestFeedReadAccessAndSchema32Upgrade(t *testing.T) {
	f := newChannelFixture(t)
	section := feedSection(t, f, "feed_read_upgrade")
	post := feedCreate(t, f, f.owner, section.Id, "legacy")
	feedReadComment(t, f, post, "legacy-comment")
	if _, err := f.store.pool.Exec(f.ctx, "DROP TABLE feed_read_state; DROP INDEX feed_comments_unread; DELETE FROM schema_migrations WHERE version=33"); err != nil {
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
			t.Fatalf("Изменены данные %s", table)
		}
	}
	api := pb.NewFeedServiceClient(f.conn)
	for _, test := range []struct {
		name, sql string
		code      codes.Code
	}{
		{"scope", "UPDATE device_grants SET scopes=ARRAY['chat.write'] WHERE principal_id=$1", codes.PermissionDenied},
		{"grant", "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", codes.Unauthenticated},
		{"session", "UPDATE auth_sessions SET expires_at=now()-interval '1 second' WHERE grant_id IN(SELECT id FROM device_grants WHERE principal_id=$1)", codes.Unauthenticated},
		{"blocked", "UPDATE memberships SET blocked=true WHERE principal_id=$1", codes.PermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			actor, id := f.enroll(t, true)
			if _, err = f.store.pool.Exec(f.ctx, test.sql, id); err != nil {
				t.Fatal(err)
			}
			_, err := api.MarkFeedPostRead(actor, &pb.MarkFeedPostReadRequest{ChannelId: section.Id, PostId: post.Id})
			requireCode(t, err, test.code)
			var count int
			if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM feed_read_state WHERE principal_id=$1", id).Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
		})
	}
	actor, _ := f.enroll(t, true)
	another := feedSection(t, f, "feed_read_other")
	_, err = api.MarkFeedPostRead(actor, &pb.MarkFeedPostReadRequest{ChannelId: another.Id, PostId: post.Id})
	requireCode(t, err, codes.NotFound)
	f.acl(t, section, roleRule("member", permissions(true, false, false, false)))
	_, err = api.MarkFeedPostRead(actor, &pb.MarkFeedPostReadRequest{ChannelId: section.Id, PostId: post.Id})
	requireCode(t, err, codes.PermissionDenied)
}
