package postgres

import (
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"testing"
)

func TestCommunityFavoritesHiddenParentAndArchive(t *testing.T) {
	f := newChannelFixture(t)
	api := pb.NewCommunityFavoriteServiceClient(f.conn)
	feed := pb.NewFeedServiceClient(f.conn)
	c := feedSection(t, f, "favorite_hidden")
	member, id := f.enroll(t, true)
	post := feedCreate(t, f, f.owner, c.Id, "post")
	comment, err := feed.CommentFeedPost(f.owner, &pb.CommentFeedPostRequest{ChannelId: c.Id, PostId: post.Id, Text: "Ответ", IdempotencyKey: "comment"})
	if err != nil {
		t.Fatal(err)
	}
	ref := reactionRef(4, comment.Comment.Id)
	request := favoriteRequest(c.Id, ref, "add", 0, true)
	if _, err = api.SetCommunityFavorite(member, request); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET scopes=ARRAY['chat.read'] WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	page, err := api.ListCommunityFavorites(member, &pb.ListCommunityFavoritesRequest{ChannelId: c.Id})
	if err != nil || len(page.GetFavorites()) != 1 || page.Favorites[0].Result.Sequence != comment.Comment.Sequence {
		t.Fatal(page, err)
	}
	if _, err = feed.ModerateFeedPost(f.owner, &pb.ModerateFeedPostRequest{ChannelId: c.Id, PostId: post.Id, Hidden: true, Reason: "Скрытый родитель", ExpectedRevision: comment.Post.Revision}); err != nil {
		t.Fatal(err)
	}
	page, err = api.ListCommunityFavorites(member, &pb.ListCommunityFavoritesRequest{ChannelId: c.Id})
	if err != nil || len(page.GetFavorites()) != 0 {
		t.Fatal("Скрытый родитель раскрыт", page, err)
	}
	states, err := api.GetCommunityFavoriteStates(member, &pb.GetCommunityFavoriteStatesRequest{ChannelId: c.Id, Contents: []*pb.CommunityContentReference{ref}})
	if err != nil || len(states.GetStates()) != 0 {
		t.Fatal(states, err)
	}
	_, err = api.SetCommunityFavorite(member, request)
	requireCode(t, err, codes.NotFound)
	removed, err := api.SetCommunityFavorite(member, favoriteRequest(c.Id, ref, "remove-hidden", 1, false))
	if err != nil || removed.State.Saved {
		t.Fatal(removed, err)
	}
	other := feedCreate(t, f, f.owner, c.Id, "other")
	_, err = f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: c.Id, Title: c.Title, Archived: true, ExpectedRevision: c.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.SetCommunityFavorite(member, favoriteRequest(c.Id, reactionRef(3, other.Id), "archive", 0, true))
	if err != nil {
		t.Fatal("Личное сохранение в архиве", err)
	}
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	_, err = api.ListCommunityFavorites(member, &pb.ListCommunityFavoritesRequest{ChannelId: c.Id})
	if err == nil {
		t.Fatal("Отозванное устройство получило избранное")
	}
}

func TestCommunityFavoritesValidationAndBudget(t *testing.T) {
	f := newChannelFixture(t)
	api := pb.NewCommunityFavoriteServiceClient(f.conn)
	c := forumSection(t, f, "favorite_budget")
	member, id := f.enroll(t, true)
	topic := createTopic(t, pb.NewForumServiceClient(f.conn), f.owner, c.Id, "topic")
	ref := reactionRef(1, topic.Id)
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	for _, refs := range [][]*pb.CommunityContentReference{nil, {ref, ref}, make([]*pb.CommunityContentReference, 51)} {
		_, err := api.GetCommunityFavoriteStates(member, &pb.GetCommunityFavoriteStatesRequest{ChannelId: c.Id, Contents: refs})
		requireCode(t, err, codes.InvalidArgument)
	}
	_, err := api.SetCommunityFavorite(member, favoriteRequest(c.Id, reactionRef(3, topic.Id), "wrong-type", 0, true))
	requireCode(t, err, codes.InvalidArgument)
	request := favoriteRequest(c.Id, ref, "first", 0, true)
	if _, err = api.SetCommunityFavorite(member, request); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.pool.Exec(f.ctx, `INSERT INTO community_favorite_requests(principal_id,channel_id,idempotency_key,request_hash,applied_revision)
 SELECT $1,$2,'budget-'||g,decode(repeat('00',32),'hex'),0 FROM generate_series(1,59)g`, id, c.Id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.SetCommunityFavorite(member, request); err != nil {
		t.Fatal("Повтор расходует бюджет", err)
	}
	_, err = api.SetCommunityFavorite(member, favoriteRequest(c.Id, ref, "over-budget", 1, false))
	requireCode(t, err, codes.ResourceExhausted)
}

func TestCommunityFavoritesQuotaNoopAndCurrentAuthor(t *testing.T) {
	f := newChannelFixture(t)
	api := pb.NewCommunityFavoriteServiceClient(f.conn)
	c := forumSection(t, f, "favorite_quota")
	member, id := f.enroll(t, true)
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	forum := pb.NewForumServiceClient(f.conn)
	one := createTopic(t, forum, f.owner, c.Id, "one")
	two := createTopic(t, forum, f.owner, c.Id, "two")
	ref := reactionRef(1, one.Id)
	if _, err := api.SetCommunityFavorite(member, favoriteRequest(c.Id, ref, "first", 0, true)); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.pool.Exec(f.ctx, `UPDATE memberships SET nickname='Новое имя автора' WHERE principal_id=$1`, one.AuthorId)
	if err != nil {
		t.Fatal(err)
	}
	page, err := api.ListCommunityFavorites(member, &pb.ListCommunityFavoritesRequest{ChannelId: c.Id})
	if err != nil || page.Favorites[0].Result.AuthorName != "Новое имя автора" {
		t.Fatal("Устаревшее имя автора", page, err)
	}
	_, err = f.store.pool.Exec(f.ctx, `INSERT INTO community_favorites(principal_id,channel_id,kind,target_id,saved,revision,saved_at) SELECT $1,$2,1,'seed-'||g,true,1,1 FROM generate_series(1,4999)g`, id, c.Id)
	if err != nil {
		t.Fatal(err)
	}
	noop, err := api.SetCommunityFavorite(member, favoriteRequest(c.Id, ref, "noop", 1, true))
	if err != nil || noop.State.Revision != 1 {
		t.Fatal("Пустая операция меняет ревизию или расходует квоту", noop, err)
	}
	_, err = api.SetCommunityFavorite(member, favoriteRequest(c.Id, reactionRef(1, two.Id), "over-quota", 0, true))
	requireCode(t, err, codes.ResourceExhausted)
	if _, err = api.SetCommunityFavorite(member, favoriteRequest(c.Id, ref, "remove", 1, false)); err != nil {
		t.Fatal(err)
	}
	if _, err = api.SetCommunityFavorite(member, favoriteRequest(c.Id, reactionRef(1, two.Id), "after-remove", 0, true)); err != nil {
		t.Fatal("Снятие отметки не освободило квоту", err)
	}
}
