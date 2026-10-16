package postgres

import (
	"sync"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

func TestMutationReceiptDoesNotOverwriteLaterChanges(t *testing.T) {
	f := newChannelFixture(t)
	created, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{
		ChannelId: "general", Text: "Первый текст", IdempotencyKey: "mutation-create"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Content.Id
	first := &pb.UpdateContentRequest{ChannelId: "general", Id: id, Text: "Правка 1", ExpectedRevision: 1, IdempotencyKey: "edit-1"}
	edited, err := f.contents.UpdateContent(f.owner, first)
	if err != nil || edited.AppliedRevision != 2 || edited.IdempotencyKey != first.IdempotencyKey {
		t.Fatalf("Нет квитанции: %v %v", edited, err)
	}
	_, err = f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: id, Text: "Правка 2", ExpectedRevision: 2, IdempotencyKey: "edit-2"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.contents.UpdateContent(f.owner, first)
	if err != nil || replay.AppliedRevision != 2 || replay.Content.Revision != 3 || replay.Content.Text != "Правка 2" {
		t.Fatalf("Повтор изменил новую версию: %v %v", replay, err)
	}
	_, err = f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: id, Text: "Подмена", ExpectedRevision: 1, IdempotencyKey: "edit-1"})
	requireCode(t, err, codes.AlreadyExists)
	_, err = f.contents.SetReaction(f.owner, &pb.SetReactionRequest{ChannelId: "general", Id: id, Emoji: "👍", Active: true, ExpectedRevision: 1, IdempotencyKey: "edit-1"})
	requireCode(t, err, codes.AlreadyExists)
	deletion := &pb.DeleteContentRequest{ChannelId: "general", Id: id, ExpectedRevision: 3, IdempotencyKey: "delete-1"}
	deleted, err := f.contents.DeleteContent(f.owner, deletion)
	if err != nil || deleted.AppliedRevision != 4 {
		t.Fatalf("Удаление: %v %v", deleted, err)
	}
	repeated, err := f.contents.DeleteContent(f.owner, deletion)
	if err != nil || repeated.AppliedRevision != 4 || !repeated.Content.Deleted {
		t.Fatalf("Повтор удаления: %v %v", repeated, err)
	}
	replay, err = f.contents.UpdateContent(f.owner, first)
	if err != nil || !replay.Content.Deleted || replay.Content.Text != "" {
		t.Fatalf("Повтор раскрыл удалённый текст: %v %v", replay, err)
	}
}

func TestReactionReceiptsPreserveLaterDecisionAndNoOp(t *testing.T) {
	f := newChannelFixture(t)
	created, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "Реакции", IdempotencyKey: "receipt-reaction"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Content.Id
	first := &pb.SetReactionRequest{ChannelId: "general", Id: id, Emoji: "👍", Active: true, ExpectedRevision: 1, IdempotencyKey: "reaction-add"}
	added, err := f.contents.SetReaction(f.owner, first)
	if err != nil || added.AppliedRevision != 2 {
		t.Fatalf("Реакция: %v %v", added, err)
	}
	noop := &pb.SetReactionRequest{ChannelId: "general", Id: id, Emoji: "👍", Active: true, ExpectedRevision: 2, IdempotencyKey: "reaction-noop"}
	same, err := f.contents.SetReaction(f.owner, noop)
	if err != nil || same.AppliedRevision != 2 {
		t.Fatalf("No-op: %v %v", same, err)
	}
	_, err = f.contents.SetReaction(f.owner, &pb.SetReactionRequest{ChannelId: "general", Id: id, Emoji: "👍", Active: false, ExpectedRevision: 2, IdempotencyKey: "reaction-remove"})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*pb.SetReactionRequest{first, noop} {
		replay, replayErr := f.contents.SetReaction(f.owner, request)
		if replayErr != nil || replay.AppliedRevision != 2 || replay.Content.Revision != 3 || len(replay.Content.Reactions) != 0 {
			t.Fatalf("Повтор отменил снятие реакции: %v %v", replay, replayErr)
		}
	}
	_, err = f.contents.SetReaction(f.owner, &pb.SetReactionRequest{ChannelId: "general", Id: id, Emoji: "🔥", Active: true, ExpectedRevision: 1, IdempotencyKey: "stale-new-reaction"})
	requireCode(t, err, codes.Aborted)
}

func TestConcurrentMutationReplayCreatesOneChange(t *testing.T) {
	f := newChannelFixture(t)
	created, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "До", IdempotencyKey: "concurrent-mutation"})
	if err != nil {
		t.Fatal(err)
	}
	request := &pb.UpdateContentRequest{ChannelId: "general", Id: created.Content.Id, Text: "После", ExpectedRevision: 1, IdempotencyKey: "same-edit"}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			response, callErr := f.contents.UpdateContent(f.owner, request)
			if callErr != nil || response.AppliedRevision != 2 || response.Content.Revision != 2 {
				t.Errorf("Повтор: %v %v", response, callErr)
			}
		})
	}
	group.Wait()
	events, err := f.sync.ListEvents(f.owner, &pb.ListEventsRequest{ChannelId: "general", After: "event-1"})
	if err != nil || len(events.Events) != 1 {
		t.Fatalf("Лишние события: %v %v", events, err)
	}
}
