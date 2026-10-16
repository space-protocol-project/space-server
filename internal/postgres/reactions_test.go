package postgres

import (
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"testing"
)

func TestReactionsPersistSyncAndRejectStaleOperations(t *testing.T) {
	f := newChannelFixture(t)
	member, _ := f.enroll(t, true)
	created, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "С реакциями", IdempotencyKey: "reactions"})
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.SetReactionRequest{ChannelId: "general", Id: created.Content.Id, Emoji: "❤️", Active: true, ExpectedRevision: 1}
	added, err := f.contents.SetReaction(member, req)
	if err != nil {
		t.Fatal(err)
	}
	if added.Content.Revision != 2 || len(added.Content.Reactions) != 1 || len(added.Content.Reactions[0].PrincipalIds) != 1 || added.Content.UpdatedAtUnixMs != 0 {
		t.Fatalf("Неверная реакция: %v", added)
	}
	repeated, err := f.contents.SetReaction(member, req)
	if err != nil || repeated.Content.Revision != 2 {
		t.Fatalf("Повтор не идемпотентен: %v %v", repeated, err)
	}
	req.Active = false
	req.ExpectedRevision = 2
	removed, err := f.contents.SetReaction(member, req)
	if err != nil || len(removed.Content.Reactions) != 0 {
		t.Fatalf("Снятие реакции: %v %v", removed, err)
	}
	req.Active = true
	req.ExpectedRevision = 1
	_, err = f.contents.SetReaction(member, req)
	requireCode(t, err, codes.Aborted)
	req.ExpectedRevision = 3
	added, err = f.contents.SetReaction(member, req)
	if err != nil {
		t.Fatal(err)
	}
	history, err := f.contents.ListContent(member, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || len(history.Contents[0].Reactions) != 1 {
		t.Fatalf("История потеряла реакции: %v %v", history, err)
	}
	events, err := f.sync.ListEvents(member, &pb.ListEventsRequest{ChannelId: "general", After: "event-1"})
	if err != nil || len(events.Events) != 3 || len(events.Events[2].Content.Reactions) != 1 {
		t.Fatalf("Синхронизация: %v %v", events, err)
	}
	req.Emoji = "<script>"
	_, err = f.contents.SetReaction(member, req)
	requireCode(t, err, codes.InvalidArgument)
	_, err = f.contents.DeleteContent(f.owner, &pb.DeleteContentRequest{ChannelId: "general", Id: req.Id, ExpectedRevision: added.Content.Revision})
	if err != nil {
		t.Fatal(err)
	}
	req.Emoji = "👍"
	_, err = f.contents.SetReaction(member, req)
	requireCode(t, err, codes.FailedPrecondition)
}

func TestReactionRequiresCurrentChannelWriteAccess(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, true)
	channel := f.create(t, "reaction_acl")
	f.acl(t, channel, principalRule(id, permissions(true, true, false, false)))
	created, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: channel.Id, Text: "Закрытая область", IdempotencyKey: "reaction-acl"})
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.SetReactionRequest{ChannelId: channel.Id, Id: created.Content.Id, Emoji: "👍", Active: true, ExpectedRevision: 1}
	_, err = f.contents.SetReaction(member, req)
	requireCode(t, err, codes.PermissionDenied)
	f.acl(t, channel, principalRule(id, permissions(true, true, true, false)))
	if _, err = f.contents.SetReaction(member, req); err != nil {
		t.Fatal(err)
	}
	f.acl(t, channel, principalRule(id, permissions(false, false, false, false)))
	req.Active = false
	req.ExpectedRevision = 2
	_, err = f.contents.SetReaction(member, req)
	requireCode(t, err, codes.NotFound)
}
