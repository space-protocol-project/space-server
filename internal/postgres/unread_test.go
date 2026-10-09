package postgres

import (
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"testing"
)

func TestMessageSequencesAndPrivateWatermarks(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, false)
	request := &pb.CreateContentRequest{ChannelId: "general", Text: "Первое сообщение", IdempotencyKey: "sequence-test"}
	first, err := f.contents.CreateContent(f.owner, request)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := f.contents.CreateContent(f.owner, request)
	if err != nil || first.Content.Sequence != 1 || repeated.Content.Sequence != 1 {
		t.Fatalf("%v %v", repeated, err)
	}
	history, err := f.contents.ListContent(member, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || history.Contents[0].Sequence != 1 {
		t.Fatalf("%v %v", history, err)
	}
	events, err := f.sync.ListEvents(member, &pb.ListEventsRequest{ChannelId: "general"})
	if err != nil || events.Events[0].Content.Sequence != 1 {
		t.Fatalf("%v %v", events, err)
	}
	secret := f.create(t, "unread-private")
	f.acl(t, secret, principalRule(id, permissions(true, false, false, false)))
	if _, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: secret.Id, Text: "Скрытый текст", IdempotencyKey: "secret-count"}); err != nil {
		t.Fatal(err)
	}
	catalog, err := f.channels.ListChannels(member, &pb.ListChannelsRequest{IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range catalog.Channels {
		if c.Id == "general" && c.LatestMessageSequence != 1 {
			t.Fatal(c)
		}
		if c.Id == secret.Id && c.LatestMessageSequence != 0 {
			t.Fatal("Количество приватных сообщений раскрыто")
		}
	}
	public, err := f.channels.GetManifest(f.ctx, &pb.GetManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range public.Channels {
		if c.LatestMessageSequence != 0 {
			t.Fatal("Количество сообщений раскрыто публично")
		}
	}
}
