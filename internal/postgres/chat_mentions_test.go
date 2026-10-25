package postgres

import (
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"testing"
)

func TestChatMentionsPersistEditReplayAndDelete(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "Маша")
	mentionAPI := pb.NewCommunityMentionServiceClient(f.conn)
	candidates, err := mentionAPI.ListMentionCandidates(f.owner, &pb.ListMentionCandidatesRequest{ChannelId: "general", Query: "МАША"})
	if err != nil || len(candidates.GetCandidates()) != 1 {
		t.Fatal(candidates, err)
	}
	req := &pb.CreateContentRequest{ChannelId: "general", Text: "@Маша привет", IdempotencyKey: "mention", Mentions: []*pb.CommunityMention{m}}
	created, err := f.contents.CreateContent(f.owner, req)
	if err != nil || len(created.GetContent().GetMentions()) != 1 {
		t.Fatal(created, err)
	}
	for _, read := range []func() ([]*pb.Content, error){
		func() ([]*pb.Content, error) {
			r, e := f.contents.ListContent(recipient, &pb.ListContentRequest{ChannelId: "general"})
			return r.GetContents(), e
		},
		func() ([]*pb.Content, error) {
			r, e := f.contents.GetHistoryPage(recipient, &pb.GetHistoryPageRequest{ChannelId: "general"})
			return r.GetContents(), e
		},
		func() ([]*pb.Content, error) {
			r, e := f.contents.SearchContent(recipient, &pb.SearchContentRequest{ChannelId: "general", Query: "привет"})
			return r.GetContents(), e
		},
	} {
		items, e := read()
		if e != nil || len(items) != 1 || len(items[0].Mentions) != 1 {
			t.Fatal(items, e)
		}
	}
	events, err := f.sync.ListEvents(recipient, &pb.ListEventsRequest{ChannelId: "general"})
	if err != nil || len(events.Events[0].Content.Mentions) != 1 {
		t.Fatal(events, err)
	}
	replay, err := f.contents.CreateContent(f.owner, req)
	if err != nil || replay.Content.Id != created.Content.Id || len(replay.Content.Mentions) != 1 {
		t.Fatal(replay, err)
	}
	conflict := pb.CreateContentRequest{ChannelId:req.ChannelId,Text:req.Text,IdempotencyKey:req.IdempotencyKey}
	_, err = f.contents.CreateContent(f.owner, &conflict)
	requireCode(t, err, codes.AlreadyExists)
	edit := &pb.UpdateContentRequest{ChannelId: "general", Id: created.Content.Id, Text: "@Маша новый текст", ExpectedRevision: 1, IdempotencyKey: "edit", Mentions: []*pb.CommunityMention{m}}
	updated, err := f.contents.UpdateContent(f.owner, edit)
	if err != nil || len(updated.GetContent().GetMentions()) != 1 {
		t.Fatal(updated, err)
	}
	edit.Mentions = nil
	_, err = f.contents.UpdateContent(f.owner, edit)
	requireCode(t, err, codes.AlreadyExists)
	updated, err = f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: created.Content.Id, Text: "просто @Маша", ExpectedRevision: 2})
	if err != nil || len(updated.GetContent().GetMentions()) != 0 {
		t.Fatal(updated, err)
	}
	deleted, err := f.contents.DeleteContent(f.owner, &pb.DeleteContentRequest{ChannelId: "general", Id: created.Content.Id, ExpectedRevision: 3})
	if err != nil || len(deleted.GetContent().GetMentions()) != 0 {
		t.Fatal(deleted, err)
	}
	// Принятый повтор возвращает текущее удалённое состояние без аннотаций.
	replay, err = f.contents.CreateContent(f.owner, req)
	if err != nil || !replay.Content.Deleted || len(replay.Content.Mentions) != 0 {
		t.Fatal(replay, err)
	}
}

func TestChatMentionsACLAndNoPartialChanges(t *testing.T) {
	f := newChannelFixture(t)
	member, m := mentionRecipient(t, f, "Участник")
	c := f.create(t, "mention_acl")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)), principalRule(m.PrincipalId, permissions(false, false, false, false)))
	api := pb.NewCommunityMentionServiceClient(f.conn)
	page, err := api.ListMentionCandidates(f.owner, &pb.ListMentionCandidatesRequest{ChannelId: c.Id, Query: "Участник"})
	if err != nil || len(page.GetCandidates()) != 0 {
		t.Fatal(page, err)
	}
	_, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: c.Id, Text: "@Участник", IdempotencyKey: "denied", Mentions: []*pb.CommunityMention{m}})
	requireCode(t, err, codes.InvalidArgument)
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	accepted, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: c.Id, Text: "@Участник", IdempotencyKey: "accepted", Mentions: []*pb.CommunityMention{m}})
	if err != nil || accepted.Content.Sequence != 1 {
		t.Fatal(accepted, err)
	}
	f.acl(t, c, principalRule(m.PrincipalId, permissions(true, true, false, false)))
	_, err = api.ListMentionCandidates(member, &pb.ListMentionCandidatesRequest{ChannelId: c.Id})
	requireCode(t, err, codes.PermissionDenied)
	// Переименование адресата не отменяет принятую операцию.
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE memberships SET nickname='Новое имя' WHERE principal_id=$1", m.PrincipalId); err != nil {
		t.Fatal(err)
	}
	_, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: c.Id, Text: "@Участник", IdempotencyKey: "accepted", Mentions: []*pb.CommunityMention{m}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: c.Id, Id: accepted.Content.Id, ExpectedRevision: 1, Text: "@Участник изменить", Mentions: []*pb.CommunityMention{m}})
	requireCode(t, err, codes.InvalidArgument)
	list, err := f.contents.ListContent(f.owner, &pb.ListContentRequest{ChannelId: c.Id})
	if err != nil || list.Contents[0].Revision != 1 || len(list.Contents[0].Mentions) != 1 {
		t.Fatal(list, err)
	}
}

func TestChatMentionsUpgradeFrom34(t *testing.T) {
	f := newChannelFixture(t)
	created, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "Сохранить", IdempotencyKey: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.pool.Exec(f.ctx, "ALTER TABLE contents DROP COLUMN mentions; DELETE FROM schema_migrations WHERE version=35")
	if err != nil {
		t.Fatal(err)
	}
	upgraded, identity, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if identity.ServerID != f.identity.ServerID {
		t.Fatal("Изменился server ID")
	}
	list, err := f.contents.ListContent(f.owner, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || list.Contents[0].Id != created.Content.Id || list.Contents[0].Text != "Сохранить" || len(list.Contents[0].Mentions) != 0 {
		t.Fatal(list, err)
	}
}
