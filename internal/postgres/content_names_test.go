package postgres

import (
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

func TestReactionNamesIncludeOnlyVisibleParticipants(t *testing.T) {
	f := newChannelFixture(t)
	member, memberID := f.enroll(t, true)
	_, outsiderID := f.enroll(t, true)
	for id, name := range map[string]string{f.ownerID: "Мария", memberID: "Илья", outsiderID: "Соня"} {
		if _, err := f.store.pool.Exec(f.ctx, "UPDATE memberships SET nickname=$2 WHERE principal_id=$1", id, name); err != nil {
			t.Fatal(err)
		}
	}
	created, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "Сообщение", IdempotencyKey: "names-create"})
	if err != nil || created.GetContent().GetParticipantNames()[f.ownerID] != "Мария" || len(created.Content.ParticipantNames) != 1 {
		t.Fatalf("Имена автора: %v %v", created, err)
	}
	reacted, err := f.contents.SetReaction(member, &pb.SetReactionRequest{ChannelId: "general", Id: created.Content.Id, Emoji: "👍", Active: true, ExpectedRevision: 1})
	if err != nil || reacted.GetContent().GetParticipantNames()[memberID] != "Илья" || len(reacted.Content.ParticipantNames) != 2 {
		t.Fatalf("Имена реакции: %v %v", reacted, err)
	}
	history, err := f.contents.ListContent(f.owner, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || history.Contents[0].ParticipantNames[outsiderID] != "" {
		t.Fatalf("История раскрыла постороннее имя: %v %v", history, err)
	}
	deleted, err := f.contents.DeleteContent(f.owner, &pb.DeleteContentRequest{ChannelId: "general", Id: created.Content.Id, ExpectedRevision: 2})
	if err != nil || len(deleted.GetContent().GetParticipantNames()) != 1 || deleted.Content.ParticipantNames[memberID] != "" {
		t.Fatalf("Удаление оставило участников прежней реакции: %v %v", deleted, err)
	}
}
