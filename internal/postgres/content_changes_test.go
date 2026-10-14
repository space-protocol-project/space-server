package postgres

import (
	"connectrpc.com/connect"
	"context"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"net/http"
	"net/http/httptest"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sync"
	"testing"
)

func TestAuthorEditsDeletesAndResumesEvents(t *testing.T) {
	f := newChannelFixture(t)
	member, _ := f.enroll(t, true)
	created, err := f.contents.CreateContent(member, &pb.CreateContentRequest{ChannelId: "general", Text: "До изменения", IdempotencyKey: "author-edit"})
	if err != nil {
		t.Fatal(err)
	}
	m := created.Content
	_, err = f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: m.Id, Text: "Чужая правка", ExpectedRevision: 1})
	requireCode(t, err, codes.PermissionDenied)
	_, err = f.contents.DeleteContent(f.owner, &pb.DeleteContentRequest{ChannelId: "general", Id: m.Id, ExpectedRevision: 1})
	requireCode(t, err, codes.PermissionDenied)
	_, err = f.contents.UpdateContent(member, &pb.UpdateContentRequest{ChannelId: "general", Id: m.Id, Text: " ", ExpectedRevision: 1})
	requireCode(t, err, codes.InvalidArgument)
	updated, err := f.contents.UpdateContent(member, &pb.UpdateContentRequest{ChannelId: "general", Id: m.Id, Text: "После изменения", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content.Revision != 2 || updated.Content.Text != "После изменения" || updated.Content.Sequence != m.Sequence || updated.Content.CreatedAtUnixMs != m.CreatedAtUnixMs || updated.Content.UpdatedAtUnixMs == 0 {
		t.Fatalf("Неверное изменение: %v", updated.Content)
	}
	_, err = f.contents.DeleteContent(member, &pb.DeleteContentRequest{ChannelId: "general", Id: m.Id, ExpectedRevision: 1})
	requireCode(t, err, codes.Aborted)
	deleted, err := f.contents.DeleteContent(member, &pb.DeleteContentRequest{ChannelId: "general", Id: m.Id, ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.Content.Deleted || deleted.Content.Text != "" || deleted.Content.Revision != 3 {
		t.Fatalf("Неверное удаление: %v", deleted.Content)
	}
	repeated, err := f.contents.CreateContent(member, &pb.CreateContentRequest{ChannelId: "general", Text: "До изменения", IdempotencyKey: "author-edit"})
	if err != nil || !repeated.Content.Deleted || repeated.Content.Revision != 3 {
		t.Fatalf("Повтор отправки потерял идемпотентность: %v %v", repeated, err)
	}
	_, err = f.contents.CreateContent(member, &pb.CreateContentRequest{ChannelId: "general", Text: "Другой запрос", IdempotencyKey: "author-edit"})
	requireCode(t, err, codes.AlreadyExists)
	_, err = f.contents.UpdateContent(member, &pb.UpdateContentRequest{ChannelId: "general", Id: m.Id, Text: "Восстановление", ExpectedRevision: 3})
	requireCode(t, err, codes.FailedPrecondition)
	next, err := f.contents.CreateContent(member, &pb.CreateContentRequest{ChannelId: "general", Text: "Следующее", IdempotencyKey: "after-edit"})
	if err != nil {
		t.Fatal(err)
	}
	if next.Content.Sequence != m.Sequence+1 {
		t.Fatal("Правки изменили порядок сообщений")
	}
	events, err := f.sync.ListEvents(member, &pb.ListEventsRequest{ChannelId: "general", After: "event-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 3 || events.Events[0].Type != "content.updated" || events.Events[1].Type != "content.deleted" || events.Events[2].Type != "content.created" || events.NextCursor != "event-4" {
		t.Fatalf("Потеряны события изменений: %v", events)
	}
	if events.Events[0].Content.Text != "" || !events.Events[0].Content.Deleted {
		t.Fatal("История событий раскрыла удалённый текст")
	}
	history, err := f.contents.ListContent(member, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || len(history.Contents) != 2 || !history.Contents[0].Deleted || history.Contents[0].Text != "" {
		t.Fatalf("Удаление не попало в историю: %v %v", history, err)
	}
	// Сохранённый ID удалённого сообщения остаётся допустимым курсором.
	page, err := f.contents.ListContent(member, &pb.ListContentRequest{ChannelId: "general", After: m.Id})
	if err != nil || len(page.Contents) != 1 || page.Contents[0].Id != next.Content.Id {
		t.Fatalf("Удаление сломало курсор: %v %v", page, err)
	}
}

func TestConcurrentEditsAndRevokedWritePermission(t *testing.T) {
	f := newChannelFixture(t)
	channel := f.create(t, "author_changes")
	member, _ := f.enroll(t, true)
	created, err := f.contents.CreateContent(member, &pb.CreateContentRequest{ChannelId: channel.Id, Text: "Оригинал", IdempotencyKey: "race"})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, text := range []string{"Первое устройство", "Второе устройство"} {
		workers.Add(1)
		go func(text string) {
			defer workers.Done()
			_, err := f.contents.UpdateContent(member, &pb.UpdateContentRequest{ChannelId: channel.Id, Id: created.Content.Id, Text: text, ExpectedRevision: 1})
			results <- err
		}(text)
	}
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch status.Code(err) {
		case codes.OK:
			successes++
		case codes.Aborted:
			conflicts++
		default:
			t.Fatalf("Неожиданный результат: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("Параллельные правки перезаписали друг друга")
	}
	f.acl(t, channel, roleRule("member", permissions(true, true, false, false)))
	_, err = f.contents.UpdateContent(member, &pb.UpdateContentRequest{ChannelId: channel.Id, Id: created.Content.Id, Text: "После отзыва", ExpectedRevision: 2})
	requireCode(t, err, codes.PermissionDenied)
	_, err = f.contents.DeleteContent(member, &pb.DeleteContentRequest{ChannelId: channel.Id, Id: created.Content.Id, ExpectedRevision: 2})
	requireCode(t, err, codes.PermissionDenied)
}

func TestContentChangesThroughConnect(t *testing.T) {
	f := newChannelFixture(t)
	web := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer web.Close()
	client := rpc.NewContentServiceClient(web.Client(), web.URL)
	create := connect.NewRequest(&pb.CreateContentRequest{ChannelId: "general", Text: "Connect", IdempotencyKey: "connect-change"})
	create.Header().Set("Authorization", outgoingAuthorization(f.owner))
	created, err := client.CreateContent(f.ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	subscription := connect.NewRequest(&pb.SubscribeRequest{ChannelId: "general", After: "event-1"})
	subscription.Header().Set("Authorization", outgoingAuthorization(f.owner))
	stream, err := rpc.NewSyncServiceClient(web.Client(), web.URL).Subscribe(ctx, subscription)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() || !stream.Msg().Heartbeat {
		t.Fatalf("Нет первого heartbeat: %v", stream.Err())
	}
	update := connect.NewRequest(&pb.UpdateContentRequest{ChannelId: "general", Id: created.Msg.Content.Id, Text: "Connect изменён", ExpectedRevision: 1})
	update.Header().Set("Authorization", outgoingAuthorization(f.owner))
	updated, err := client.UpdateContent(f.ctx, update)
	if err != nil || updated.Msg.Content.Text != "Connect изменён" {
		t.Fatalf("Connect UpdateContent: %v %v", updated, err)
	}
	if !stream.Receive() || stream.Msg().Event.Type != "content.updated" || stream.Msg().Event.Content.Text != "Connect изменён" {
		t.Fatalf("Не доставлена правка: %v", stream.Err())
	}
	remove := connect.NewRequest(&pb.DeleteContentRequest{ChannelId: "general", Id: created.Msg.Content.Id, ExpectedRevision: 2})
	remove.Header().Set("Authorization", outgoingAuthorization(f.owner))
	deleted, err := client.DeleteContent(f.ctx, remove)
	if err != nil || !deleted.Msg.Content.Deleted || deleted.Msg.Content.Text != "" {
		t.Fatalf("Connect DeleteContent: %v %v", deleted, err)
	}
	if !stream.Receive() || stream.Msg().Event.Type != "content.deleted" || !stream.Msg().Event.Content.Deleted || stream.Msg().Event.Content.Text != "" {
		t.Fatalf("Не доставлено удаление: %v", stream.Err())
	}

}
