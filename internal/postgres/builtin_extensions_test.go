package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/builtin"
	"github.com/space-protocol-project/space-server/internal/voice"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

func builtinContext(t *testing.T, f *channelFixture, source context.Context) context.Context {
	t.Helper()
	ctx, err := authn.AuthorizeCall(metadata.NewIncomingContext(f.ctx, metadata.Pairs("authorization", outgoingAuthorization(source))), f.store, "/space.v1.ChannelService/ListChannels")
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestBuiltinRegistryAdapters(t *testing.T) {
	f := newChannelFixture(t)
	chat := f.create(t, "builtin_chat")
	if _, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: chat.Id, Text: "Прежнее сообщение", IdempotencyKey: "builtin-chat"}); err != nil {
		t.Fatal(err)
	}
	forum := forumSection(t, f, "builtin_forum")
	createTopic(t, pb.NewForumServiceClient(f.conn), f.owner, forum.Id, "builtin-topic")
	feed := feedSection(t, f, "builtin_feed")
	feedCreate(t, f, f.owner, feed.Id, "builtin-post")
	room, err := f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: "builtin_room", Title: "Комната", ViewType: "voice_room"})
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := voice.NewService(f.ctx, f.store)
	if err != nil {
		t.Fatal(err)
	}
	registry := builtin.New(f.store, rooms)
	ctx := builtinContext(t, f, f.owner)
	expected := map[string]proto.Message{}
	expected[chat.Id], err = f.store.List(ctx, &pb.ListContentRequest{ChannelId: chat.Id})
	if err != nil {
		t.Fatal(err)
	}
	expected[forum.Id], err = f.store.ListForumThreads(ctx, &pb.ListForumThreadsRequest{ChannelId: forum.Id, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	expected[feed.Id], err = f.store.ListFeedPosts(ctx, &pb.ListFeedPostsRequest{ChannelId: feed.Id, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	expected[room.Channel.Id], err = rooms.GetVoiceRoom(ctx, &pb.GetVoiceRoomRequest{ChannelId: room.Channel.Id})
	if err != nil {
		t.Fatal(err)
	}
	sections, err := registry.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, section := range sections {
		want, ok := expected[section.Channel.Id]
		if !ok {
			continue
		}
		result, err := registry.Open(ctx, section.InstanceID)
		if err != nil || !proto.Equal(result.Content, want) {
			t.Fatalf("Адаптер %s: %v %v", section.ViewID, result, err)
		}
		if section.Installation.Version != "legacy.p2" || section.ViewID != section.Channel.Views[0].Id {
			t.Fatal(section)
		}
		seen++
	}
	if seen != 4 {
		t.Fatal("Не все адаптеры", seen)
	}
	reopened, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.BuiltinSections(ctx)
	if err != nil || len(restored) != len(sections) {
		t.Fatal("Реестр после открытия", err)
	}
	for i := range restored {
		if restored[i].InstanceID != sections[i].InstanceID {
			t.Fatal("Изменился ID")
		}
	}
}

func TestBuiltinRegistryAuthority(t *testing.T) {
	f := newChannelFixture(t)
	channel := f.create(t, "builtin_acl")
	member, id := f.enroll(t, false)
	ctx := builtinContext(t, f, member)
	registry := builtin.New(f.store, nil)
	instance := "builtin:" + channel.Id + ":chat"
	if _, err := registry.Open(ctx, instance); err != nil {
		t.Fatal(err)
	}
	f.acl(t, channel, principalRule(id, permissions(false, false, false, false)))
	_, err := registry.Open(ctx, instance)
	requireCode(t, err, codes.NotFound)
	sections, err := registry.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sections {
		if s.InstanceID == instance {
			t.Fatal("Раскрыт скрытый раздел")
		}
	}
	_, err = registry.List(context.Background())
	requireCode(t, err, codes.Unauthenticated)
	_, err = registry.Open(ctx, "builtin:"+channel.Id+":forum")
	requireCode(t, err, codes.NotFound)
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	_, err = registry.List(ctx)
	requireCode(t, err, codes.Unauthenticated)
}

func TestBuiltinRegistryVisibility(t *testing.T) {
	f := newChannelFixture(t)
	first := f.create(t, "builtin_first")
	second := f.create(t, "builtin_second")
	registry := builtin.New(f.store, nil)
	ctx := builtinContext(t, f, f.owner)
	a, err := f.store.BuiltinSection(ctx, "builtin:"+first.Id+":chat")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.store.BuiltinSection(ctx, "builtin:"+second.Id+":chat")
	if err != nil {
		t.Fatal(err)
	}
	if a.InstanceID == b.InstanceID || a.Installation != b.Installation {
		t.Fatal("Неверная связь экземпляров")
	}
	if _, err = f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: first.Id, Title: "Новое имя", Archived: true, ExpectedRevision: first.Revision}); err != nil {
		t.Fatal(err)
	}
	_, err = registry.Open(ctx, a.InstanceID)
	requireCode(t, err, codes.FailedPrecondition)
	sections, err := registry.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range sections {
		if section.InstanceID == a.InstanceID {
			t.Fatal("Архивный раздел виден")
		}
	}
	other := newChannelFixture(t)
	_, err = other.store.BuiltinSections(ctx)
	requireCode(t, err, codes.Unauthenticated)
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE builtin_extension_instances SET installation_id='builtin:feed' WHERE id=$1", b.InstanceID); err == nil {
		t.Fatal("Разрешена подмена типа адаптера")
	}
}

func TestBuiltinRegistryTransaction(t *testing.T) {
	f := newChannelFixture(t)
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err = tx.Exec(f.ctx, "INSERT INTO channels(id,title,view_type) VALUES('rolled_back','Откат','feed')"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = tx.QueryRow(f.ctx, "SELECT count(*) FROM builtin_extension_instances WHERE channel_id='rolled_back'").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM builtin_extension_instances WHERE channel_id='rolled_back'").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	channel := f.create(t, "builtin_delete")
	if _, err = f.channels.DeleteChannel(f.owner, &pb.DeleteChannelRequest{ChannelId: channel.Id, ExpectedRevision: channel.Revision}); err != nil {
		t.Fatal(err)
	}
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM builtin_extension_instances WHERE channel_id=$1", channel.Id).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestBuiltinRegistryUpgrade(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	identity := seedVersion12(t, ctx, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for i := 12; i < 46; i++ {
		if err = applyMigration(ctx, tx, i+1, migrationFiles[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO channels(id,title,view_type) VALUES('old_forum','Прежний форум','forum'),('old_feed','Прежняя лента','feed'),('old_room','Прежняя комната','voice_room')"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before := legacySnapshot(t, ctx, pool)
	store, after, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if identity.ServerID != after.ServerID {
		t.Fatal("Изменилась идентичность")
	}
	snapshot := legacySnapshot(t, ctx, pool)
	for table, value := range before {
		if snapshot[table] != value {
			t.Fatal("Изменены данные", table)
		}
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM channels c LEFT JOIN builtin_extension_instances i ON i.channel_id=c.id AND i.view_id=c.view_type WHERE i.id IS NULL`).Scan(&count); err != nil || count != 0 {
		t.Fatal("Неполный backfill", count, err)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM builtin_extension_installations").Scan(&count); err != nil || count != 4 {
		t.Fatal(count, err)
	}
}
