package postgres

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func reactionRef(kind int32, id string) *pb.CommunityContentReference {
	return &pb.CommunityContentReference{Kind: pb.CommunitySearchKind(kind), Id: id}
}
func reactionRequest(channel string, ref *pb.CommunityContentReference, key string, revision int64, active bool) *pb.SetCommunityReactionRequest {
	return &pb.SetCommunityReactionRequest{ChannelId: channel, Content: ref, Emoji: "👍", Active: active, ExpectedRevision: revision, IdempotencyKey: key}
}
func readCommunityReaction(t *testing.T, api pb.CommunityReactionServiceClient, ctx context.Context, channel string, ref *pb.CommunityContentReference) *pb.CommunityReactionState {
	t.Helper()
	r, err := api.ListCommunityReactions(ctx, &pb.ListCommunityReactionsRequest{ChannelId: channel, Contents: []*pb.CommunityContentReference{ref}})
	if err != nil || len(r.GetStates()) != 1 {
		t.Fatalf("Чтение реакций: %v %v", r, err)
	}
	return r.States[0]
}

func TestCommunityReactionsFourTargetsReplayAndIsolation(t *testing.T) {
	f := newChannelFixture(t)
	api := pb.NewCommunityReactionServiceClient(f.conn)
	forum := pb.NewForumServiceClient(f.conn)
	feed := pb.NewFeedServiceClient(f.conn)
	c := forumSection(t, f, "reaction_forum")
	member, _ := f.enroll(t, true)
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	topic := createTopic(t, forum, f.owner, c.Id, "topic")
	reply, err := forum.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: c.Id, ThreadId: topic.Id, Text: "Ответ", IdempotencyKey: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	fc := feedSection(t, f, "reaction_feed")
	feedPolicy(t, f, fc, feedRole("member", false, true))
	post := feedCreate(t, f, f.owner, fc.Id, "post")
	comment, err := feed.CommentFeedPost(f.owner, &pb.CommentFeedPostRequest{ChannelId: fc.Id, PostId: post.Id, Text: "Комментарий", IdempotencyKey: "comment"})
	if err != nil {
		t.Fatal(err)
	}
	beforeForum, err := forum.GetForumThread(member, &pb.GetForumThreadRequest{ChannelId: c.Id, ThreadId: topic.Id})
	if err != nil {
		t.Fatal(err)
	}
	beforeFeed, err := feed.GetFeedPost(member, &pb.GetFeedPostRequest{ChannelId: fc.Id, PostId: post.Id})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		channel string
		ref     *pb.CommunityContentReference
	}{
		{c.Id, reactionRef(1, topic.Id)}, {c.Id, reactionRef(2, reply.Reply.Id)},
		{fc.Id, reactionRef(3, post.Id)}, {fc.Id, reactionRef(4, comment.Comment.Id)},
	} {
		initial := readCommunityReaction(t, api, member, target.channel, target.ref)
		if initial.Revision != 0 || !initial.CanReact || len(initial.Reactions) != 0 {
			t.Fatal(initial)
		}
		req := reactionRequest(target.channel, target.ref, "add-"+target.ref.Id, 0, true)
		added, err := api.SetCommunityReaction(member, req)
		if err != nil || added.State.Revision != 1 || added.State.Reactions[0].Count != 1 || !added.State.Reactions[0].Selected {
			t.Fatalf("Добавление: %v %v", added, err)
		}
		stranger := readCommunityReaction(t, api, f.owner, target.channel, target.ref)
		if stranger.Reactions[0].Selected {
			t.Fatal("Чужая реакция стала собственной")
		}
		removed, err := api.SetCommunityReaction(member, reactionRequest(target.channel, target.ref, "remove-"+target.ref.Id, 1, false))
		if err != nil || len(removed.State.Reactions) != 0 || removed.State.Revision != 2 {
			t.Fatalf("Удаление: %v %v", removed, err)
		}
		replay, err := api.SetCommunityReaction(member, req)
		if err != nil || replay.AppliedRevision != 1 || replay.State.Revision != 2 || len(replay.State.Reactions) != 0 {
			t.Fatalf("Повтор отменил новое решение: %v %v", replay, err)
		}
		changed := proto.Clone(req).(*pb.SetCommunityReactionRequest)
		changed.Emoji = "❤️"
		_, err = api.SetCommunityReaction(member, changed)
		requireCode(t, err, codes.AlreadyExists)
	}
	afterForum, err := forum.GetForumThread(member, &pb.GetForumThreadRequest{ChannelId: c.Id, ThreadId: topic.Id})
	if err != nil || !proto.Equal(beforeForum, afterForum) {
		t.Fatal("Реакции изменили тему, порядок или непрочитанное", err)
	}
	afterFeed, err := feed.GetFeedPost(member, &pb.GetFeedPostRequest{ChannelId: fc.Id, PostId: post.Id})
	if err != nil || !proto.Equal(beforeFeed, afterFeed) {
		t.Fatal("Реакции изменили публикацию или непрочитанное", err)
	}
}

func TestCommunityReactionsConcurrentRevisionAndSameKey(t *testing.T) {
	f := newChannelFixture(t)
	c := forumSection(t, f, "reaction_race")
	member, _ := f.enroll(t, true)
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	topic := createTopic(t, pb.NewForumServiceClient(f.conn), f.owner, c.Id, "topic")
	api := pb.NewCommunityReactionServiceClient(f.conn)
	ref := reactionRef(1, topic.Id)
	var wg sync.WaitGroup
	outcomes := make(chan codes.Code, 2)
	for _, ctx := range []context.Context{f.owner, member} {
		wg.Add(1)
		go func(ctx context.Context) {
			defer wg.Done()
			_, err := api.SetCommunityReaction(ctx, reactionRequest(c.Id, ref, "simultaneous", 0, true))
			outcomes <- status.Code(err)
		}(ctx)
	}
	wg.Wait()
	close(outcomes)
	counts := map[codes.Code]int{}
	for code := range outcomes {
		counts[code]++
	}
	if counts[codes.OK] != 1 || counts[codes.Aborted] != 1 {
		t.Fatal(counts)
	}
	current := readCommunityReaction(t, api, f.owner, c.Id, ref)
	if current.Revision != 1 || current.Reactions[0].Count != 1 {
		t.Fatal(current)
	}
	// Разные одновременные повторы одной операции дают одну запись и одну ревизию.
	req := reactionRequest(c.Id, ref, "same-key", 1, true)
	req.Emoji = "🔥"
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := api.SetCommunityReaction(f.owner, req); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	current = readCommunityReaction(t, api, f.owner, c.Id, ref)
	if current.Revision != 2 || len(current.Reactions) != 2 {
		t.Fatal(current)
	}
}

func TestCommunityReactionsRightsVisibilityAndClosed(t *testing.T) {
	f := newChannelFixture(t)
	api := pb.NewCommunityReactionServiceClient(f.conn)
	forum := pb.NewForumServiceClient(f.conn)
	c := forumSection(t, f, "reaction_acl")
	member, id := f.enroll(t, true)
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	topic := createTopic(t, forum, f.owner, c.Id, "topic")
	ref := reactionRef(1, topic.Id)
	if readCommunityReaction(t, api, member, c.Id, ref).CanReact {
		t.Fatal("Читателю выдана запись")
	}
	req := reactionRequest(c.Id, ref, "one", 0, true)
	_, err := api.SetCommunityReaction(member, req)
	requireCode(t, err, codes.PermissionDenied)
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)), roleRule("reader", permissions(true, true, false, false)))
	if _, err = api.SetCommunityReaction(member, req); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET scopes=ARRAY['chat.read'] WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	if readCommunityReaction(t, api, member, c.Id, ref).CanReact {
		t.Fatal("Проигнорирован scope устройства")
	}
	_, err = api.SetCommunityReaction(member, req)
	requireCode(t, err, codes.PermissionDenied)
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET scopes=ARRAY['chat.read','chat.write'] WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE memberships SET role='reader' WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	_, err = api.SetCommunityReaction(member, req)
	requireCode(t, err, codes.PermissionDenied)
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE memberships SET role='member',blocked=true WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	_, err = api.ListCommunityReactions(member, &pb.ListCommunityReactionsRequest{ChannelId: c.Id, Contents: []*pb.CommunityContentReference{ref}})
	requireCode(t, err, codes.PermissionDenied)
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE memberships SET blocked=false WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	closed, err := forum.CloseForumThread(f.owner, &pb.CloseForumThreadRequest{ChannelId: c.Id, ThreadId: topic.Id, ExpectedRevision: topic.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if readCommunityReaction(t, api, f.owner, c.Id, ref).CanReact {
		t.Fatal("Закрытая тема разрешает реакции")
	}
	_, err = api.SetCommunityReaction(f.owner, reactionRequest(c.Id, ref, "closed", 1, true))
	requireCode(t, err, codes.FailedPrecondition)
	_, err = forum.ModerateForumThread(f.owner, &pb.ModerateForumThreadRequest{ChannelId: c.Id, ThreadId: topic.Id, Action: "hide", Reason: "Проверка", ExpectedRevision: closed.Thread.Revision})
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := api.ListCommunityReactions(f.owner, &pb.ListCommunityReactionsRequest{ChannelId: c.Id, Contents: []*pb.CommunityContentReference{ref}})
	if err != nil || len(hidden.States) != 0 {
		t.Fatal("Раскрыты реакции скрытого объекта", hidden, err)
	}
	_, err = api.SetCommunityReaction(f.owner, req)
	requireCode(t, err, codes.NotFound)
}

func TestCommunityReactionsFeedPolicyParentAndArchive(t *testing.T) {
	f := newChannelFixture(t)
	api := pb.NewCommunityReactionServiceClient(f.conn)
	feed := pb.NewFeedServiceClient(f.conn)
	c := feedSection(t, f, "reaction_feed_acl")
	member, _ := f.enroll(t, true)
	post := feedCreate(t, f, f.owner, c.Id, "post")
	comment, err := feed.CommentFeedPost(f.owner, &pb.CommentFeedPostRequest{ChannelId: c.Id, PostId: post.Id, Text: "Текст", IdempotencyKey: "comment"})
	if err != nil {
		t.Fatal(err)
	}
	ref := reactionRef(4, comment.Comment.Id)
	req := reactionRequest(c.Id, ref, "react", 0, true)
	_, err = api.SetCommunityReaction(member, req)
	requireCode(t, err, codes.PermissionDenied)
	feedPolicy(t, f, c, feedRole("member", false, true))
	if _, err = api.SetCommunityReaction(member, req); err != nil {
		t.Fatal(err)
	}
	if _, err = feed.ModerateFeedPost(f.owner, &pb.ModerateFeedPostRequest{ChannelId: c.Id, PostId: post.Id, Hidden: true, Reason: "Скрыть родителя", ExpectedRevision: comment.Post.Revision}); err != nil {
		t.Fatal(err)
	}
	page, err := api.ListCommunityReactions(f.owner, &pb.ListCommunityReactionsRequest{ChannelId: c.Id, Contents: []*pb.CommunityContentReference{ref, reactionRef(3, post.Id)}})
	if err != nil || len(page.States) != 0 {
		t.Fatal("Скрытый родитель раскрыл реакции", page, err)
	}
	_, err = api.SetCommunityReaction(member, req)
	requireCode(t, err, codes.NotFound)
	another := feedCreate(t, f, f.owner, c.Id, "other")
	_, err = f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: c.Id, Title: c.Title, Archived: true, ExpectedRevision: c.Revision})
	if err != nil {
		t.Fatal(err)
	}
	ref = reactionRef(3, another.Id)
	if readCommunityReaction(t, api, f.owner, c.Id, ref).CanReact {
		t.Fatal("Архив разрешает реакции")
	}
	_, err = api.SetCommunityReaction(f.owner, reactionRequest(c.Id, ref, "archived", 0, true))
	requireCode(t, err, codes.FailedPrecondition)
}

func TestCommunityReactionsLimitsAndSharedBudget(t *testing.T) {
	f := newChannelFixture(t)
	api := pb.NewCommunityReactionServiceClient(f.conn)
	c := forumSection(t, f, "reaction_limits")
	topic := createTopic(t, pb.NewForumServiceClient(f.conn), f.owner, c.Id, "one")
	ref := reactionRef(1, topic.Id)
	bad := reactionRequest(c.Id, ref, "invalid", 0, true)
	bad.Emoji = "arbitrary text"
	_, err := api.SetCommunityReaction(f.owner, bad)
	requireCode(t, err, codes.InvalidArgument)
	_, err = api.ListCommunityReactions(f.owner, &pb.ListCommunityReactionsRequest{ChannelId: c.Id, Contents: []*pb.CommunityContentReference{ref, ref}})
	requireCode(t, err, codes.InvalidArgument)
	catalogue, err := os.ReadFile("../reactions/emoji.txt")
	if err != nil {
		t.Fatal(err)
	}
	emojis := strings.Fields(string(catalogue))
	for i := 0; i < 33; i++ {
		req := reactionRequest(c.Id, ref, "emoji-"+emojis[i], int64(i), true)
		req.Emoji = emojis[i]
		_, err = api.SetCommunityReaction(f.owner, req)
		if i == 32 {
			requireCode(t, err, codes.ResourceExhausted)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	second := forumSection(t, f, "reaction_budget")
	target := createTopic(t, pb.NewForumServiceClient(f.conn), f.owner, second.Id, "two")
	for i := 32; i < 61; i++ {
		// Пустое состояние тоже имеет ключ повтора и расходует общий бюджет.
		_, err = api.SetCommunityReaction(f.owner, reactionRequest(second.Id, reactionRef(1, target.Id), "budget-"+emojis[i], 0, false))
		if i == 60 {
			requireCode(t, err, codes.ResourceExhausted)
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCommunityReactionsConnectAuthAndACLOnReplay(t *testing.T) {
	f := newChannelFixture(t)
	c := forumSection(t, f, "reaction_connect")
	member, _ := f.enroll(t, true)
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	topic := createTopic(t, pb.NewForumServiceClient(f.conn), f.owner, c.Id, "topic")
	server := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer server.Close()
	api := rpc.NewCommunityReactionServiceClient(server.Client(), server.URL, connect.WithProtoJSON())
	req := connect.NewRequest(reactionRequest(c.Id, reactionRef(1, topic.Id), "connect", 0, true))
	md, _ := metadata.FromOutgoingContext(member)
	req.Header().Set("Authorization", md.Get("authorization")[0])
	response, err := api.SetCommunityReaction(f.ctx, req)
	if err != nil || response.Msg.State.Reactions[0].Count != 1 {
		t.Fatal(response, err)
	}
	f.acl(t, c, roleRule("member", permissions(false, false, false, false)))
	_, err = api.SetCommunityReaction(f.ctx, req)
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal("Повтор обошёл новый ACL", err)
	}
}
