package postgres

import (
	"strings"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

func TestReplySurvivesReplayChangesEventsAndDeletion(t *testing.T) {
	f := newChannelFixture(t)
	manifest, err := f.channels.GetManifest(f.owner, &pb.GetManifestRequest{})
	if err != nil || !manifest.GetOrdinaryRepliesSupported() {
		t.Fatalf("Поддержка ответов не объявлена: %v %v", manifest, err)
	}
	parent, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "Исходный текст", IdempotencyKey: "reply-parent"})
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.CreateContentRequest{ChannelId: "general", Text: "Ответ", IdempotencyKey: "reply-child", ReplyToId: parent.Content.Id}
	child, err := f.contents.CreateContent(f.owner, req)
	if err != nil || child.GetContent().GetReplyToId() != parent.Content.Id {
		t.Fatalf("Ответ: %v %v", child, err)
	}
	replay, err := f.contents.CreateContent(f.owner, req)
	if err != nil || replay.Content.Id != child.Content.Id || replay.Content.ReplyToId != parent.Content.Id {
		t.Fatalf("Повтор ответа: %v %v", replay, err)
	}
	_, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: req.ChannelId, Text: req.Text, IdempotencyKey: req.IdempotencyKey})
	requireCode(t, err, codes.AlreadyExists)
	edited, err := f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: child.Content.Id, Text: "Новый ответ", ExpectedRevision: 1})
	if err != nil || edited.GetContent().GetReplyToId() != parent.Content.Id {
		t.Fatalf("Правка потеряла ссылку: %v %v", edited, err)
	}
	_, err = f.contents.DeleteContent(f.owner, &pb.DeleteContentRequest{ChannelId: "general", Id: parent.Content.Id, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	history, err := f.contents.ListContent(f.owner, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || len(history.GetContents()) != 2 || history.Contents[1].ReplyToId != parent.Content.Id || history.Contents[0].Text != "" || !history.Contents[0].Deleted {
		t.Fatalf("История после удаления: %v %v", history, err)
	}
	events, err := f.sync.ListEvents(f.owner, &pb.ListEventsRequest{ChannelId: "general"})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events.Events {
		if event.Content.Id == child.Content.Id && event.Content.ReplyToId != parent.Content.Id {
			t.Fatal("Событие потеряло ответ")
		}
		if event.Content.Id == parent.Content.Id && event.Content.Text != "" {
			t.Fatal("Событие раскрыло удалённый текст")
		}
	}
	// Повтор после удаления цели возвращает прежний ответ, без новой записи.
	replay, err = f.contents.CreateContent(f.owner, req)
	if err != nil || replay.Content.Id != child.Content.Id {
		t.Fatalf("Повтор после удаления цели: %v %v", replay, err)
	}
}

func TestReplyTargetStaysWithinReadableChannel(t *testing.T) {
	f := newChannelFixture(t)
	secret := f.create(t, "reply_secret")
	_, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: secret.Id, Text: "Секрет", IdempotencyKey: "secret-parent"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "Нельзя сослаться на чужой канал", IdempotencyKey: "cross-channel", ReplyToId: "message-1"})
	requireCode(t, err, codes.FailedPrecondition)
	_, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "Длинная ссылка", IdempotencyKey: "long-reply", ReplyToId: strings.Repeat("a", 129)})
	requireCode(t, err, codes.InvalidArgument)
	member, principal := f.enroll(t, true)
	f.acl(t, secret, principalRule(principal, permissions(false, false, false, false)))
	_, err = f.contents.CreateContent(member, &pb.CreateContentRequest{ChannelId: secret.Id, Text: "Нет доступа", IdempotencyKey: "denied-reply", ReplyToId: "message-1"})
	requireCode(t, err, codes.NotFound)
	history, err := f.contents.ListContent(f.owner, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || len(history.GetContents()) != 0 {
		t.Fatalf("Отказ создал сообщение: %v %v", history, err)
	}
}
