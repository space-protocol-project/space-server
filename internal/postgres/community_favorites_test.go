package postgres

import (
	"context"
	"sync"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func favoriteRequest(channel string, ref *pb.CommunityContentReference, key string, revision int64, saved bool) *pb.SetCommunityFavoriteRequest {
	return &pb.SetCommunityFavoriteRequest{ChannelId: channel, Content: ref, Saved: saved, ExpectedRevision: revision, IdempotencyKey: key}
}
func favoriteRead(t *testing.T, api pb.CommunityFavoriteServiceClient, ctx context.Context, channel string, ref *pb.CommunityContentReference) *pb.CommunityFavoriteState {
	t.Helper()
	response, err := api.GetCommunityFavoriteStates(ctx, &pb.GetCommunityFavoriteStatesRequest{ChannelId: channel, Contents: []*pb.CommunityContentReference{ref}})
	if err != nil || len(response.GetStates()) != 1 {
		t.Fatalf("Чтение избранного: %v %v", response, err)
	}
	return response.States[0]
}

func TestCommunityFavoritesFourTargetsPrivateAndReplay(t *testing.T) {
	f := newChannelFixture(t)
	api := pb.NewCommunityFavoriteServiceClient(f.conn)
	forum := pb.NewForumServiceClient(f.conn)
	feed := pb.NewFeedServiceClient(f.conn)
	c := forumSection(t, f, "favorite_forum")
	fc := feedSection(t, f, "favorite_feed")
	member, _ := f.enroll(t, true)
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	feedPolicy(t, f, fc, feedRole("member", false, false))
	topic := createTopic(t, forum, f.owner, c.Id, "topic")
	reply, err := forum.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: c.Id, ThreadId: topic.Id, Text: "Ответ", IdempotencyKey: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	post := feedCreate(t, f, f.owner, fc.Id, "post")
	comment, err := feed.CommentFeedPost(f.owner, &pb.CommentFeedPostRequest{ChannelId: fc.Id, PostId: post.Id, Text: "Комментарий", IdempotencyKey: "comment"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := forum.GetForumThread(member, &pb.GetForumThreadRequest{ChannelId: c.Id, ThreadId: topic.Id})
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
		initial := favoriteRead(t, api, member, target.channel, target.ref)
		if initial.Saved || initial.Revision != 0 {
			t.Fatal(initial)
		}
		request := favoriteRequest(target.channel, target.ref, "add-"+target.ref.Id, 0, true)
		added, err := api.SetCommunityFavorite(member, request)
		if err != nil || !added.GetState().GetSaved() || added.State.Revision != 1 {
			t.Fatalf("Добавление: %v %v", added, err)
		}
		other := favoriteRead(t, api, f.owner, target.channel, target.ref)
		if other.Saved || other.Revision != 0 {
			t.Fatal("Раскрыто чужое избранное", other)
		}
		list, err := api.ListCommunityFavorites(f.owner, &pb.ListCommunityFavoritesRequest{ChannelId: target.channel})
		if err != nil || len(list.GetFavorites()) != 0 {
			t.Fatal("Чужой список", list, err)
		}
		removed, err := api.SetCommunityFavorite(member, favoriteRequest(target.channel, target.ref, "remove-"+target.ref.Id, 1, false))
		if err != nil || removed.State.Saved || removed.State.Revision != 2 {
			t.Fatal(removed, err)
		}
		replay, err := api.SetCommunityFavorite(member, request)
		if err != nil || replay.AppliedRevision != 1 || replay.State.Revision != 2 || replay.State.Saved {
			t.Fatal("Повтор отменил новое решение", replay, err)
		}
		changed := proto.Clone(request).(*pb.SetCommunityFavoriteRequest)
		changed.Saved = false
		_, err = api.SetCommunityFavorite(member, changed)
		requireCode(t, err, codes.AlreadyExists)
		_, err = api.SetCommunityFavorite(member, favoriteRequest(target.channel, target.ref, "stale", 0, true))
		requireCode(t, err, codes.Aborted)
	}
	after, err := forum.GetForumThread(member, &pb.GetForumThreadRequest{ChannelId: c.Id, ThreadId: topic.Id})
	if err != nil || !proto.Equal(before, after) {
		t.Fatal("Избранное изменило содержимое или непрочитанное", err)
	}
}

func TestCommunityFavoritesPagingConcurrentAndAccess(t *testing.T) {
	f := newChannelFixture(t)
	api := pb.NewCommunityFavoriteServiceClient(f.conn)
	forum := pb.NewForumServiceClient(f.conn)
	c := forumSection(t, f, "favorite_pages")
	member, _ := f.enroll(t, true)
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	var refs []*pb.CommunityContentReference
	for _, key := range []string{"one", "two", "three"} {
		topic := createTopic(t, forum, f.owner, c.Id, key)
		ref := reactionRef(1, topic.Id)
		refs = append(refs, ref)
		request := favoriteRequest(c.Id, ref, key, 0, true)
		var wg sync.WaitGroup
		failures := make(chan error, 6)
		for range 6 {
			wg.Go(func() { _, err := api.SetCommunityFavorite(member, request); failures <- err })
		}
		wg.Wait()
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
		if favoriteRead(t, api, member, c.Id, ref).Revision != 1 {
			t.Fatal("Повторы изменили ревизию")
		}
	}
	first, err := api.ListCommunityFavorites(member, &pb.ListCommunityFavoritesRequest{ChannelId: c.Id, Limit: 1})
	if err != nil || len(first.GetFavorites()) != 1 || first.NextPageToken == "" {
		t.Fatal(first, err)
	}
	seen := map[string]bool{first.Favorites[0].Result.Id: true}
	token := first.NextPageToken
	for token != "" {
		page, err := api.ListCommunityFavorites(member, &pb.ListCommunityFavoritesRequest{ChannelId: c.Id, Limit: 1, PageToken: token})
		if err != nil || len(page.GetFavorites()) != 1 || seen[page.Favorites[0].Result.Id] {
			t.Fatal(page, err)
		}
		seen[page.Favorites[0].Result.Id] = true
		token = page.NextPageToken
	}
	if len(seen) != 3 {
		t.Fatal(seen)
	}
	_, err = api.ListCommunityFavorites(f.owner, &pb.ListCommunityFavoritesRequest{ChannelId: c.Id, PageToken: first.NextPageToken})
	requireCode(t, err, codes.InvalidArgument)
	// Закрытие не запрещает личное сохранение при наличии чтения.
	topic, err := forum.GetForumThread(f.owner, &pb.GetForumThreadRequest{ChannelId: c.Id, ThreadId: refs[0].Id})
	if err != nil {
		t.Fatal(err)
	}
	_, err = forum.CloseForumThread(f.owner, &pb.CloseForumThreadRequest{ChannelId: c.Id, ThreadId: refs[0].Id, ExpectedRevision: topic.Thread.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.SetCommunityFavorite(member, favoriteRequest(c.Id, refs[0], "closed-remove", 1, false))
	if err != nil {
		t.Fatal("Закрытая тема", err)
	}
	_, err = api.SetCommunityFavorite(member, favoriteRequest(c.Id, refs[0], "closed-add", 2, true))
	if err != nil {
		t.Fatal("Сохранение закрытой темы", err)
	}
	f.acl(t, c, roleRule("member", permissions(true, false, false, false)))
	_, err = api.ListCommunityFavorites(member, &pb.ListCommunityFavoritesRequest{ChannelId: c.Id})
	requireCode(t, err, codes.PermissionDenied)
	_, err = api.GetCommunityFavoriteStates(member, &pb.GetCommunityFavoriteStatesRequest{ChannelId: c.Id, Contents: refs})
	requireCode(t, err, codes.PermissionDenied)
	_, err = api.SetCommunityFavorite(member, favoriteRequest(c.Id, refs[0], "closed-add", 2, true))
	requireCode(t, err, codes.PermissionDenied)
}
