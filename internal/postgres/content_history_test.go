package postgres

import (
	"bytes"
	"fmt"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

func TestHistoryPagesBeyond1100AndEventBoundary(t *testing.T) {
	f := newChannelFixture(t)
	var first *pb.Content
	for i := 1; i <= 1205; i++ {
		created, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: fmt.Sprintf("История %d", i), IdempotencyKey: fmt.Sprintf("history-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			first = created.Content
		}
	}
	page, err := f.contents.GetHistoryPage(f.owner, &pb.GetHistoryPageRequest{ChannelId: "general"})
	if err != nil || len(page.GetContents()) != 100 || page.Contents[0].Sequence != 1106 || page.NextBeforeSequence != 1106 || page.EventCursor == "" {
		t.Fatalf("Первая страница: %v %v", page, err)
	}
	boundary := page.EventCursor
	_, err = f.contents.DeleteContent(f.owner, &pb.DeleteContentRequest{ChannelId: "general", Id: first.Id, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	added, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "После снимка", IdempotencyKey: "after-snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := f.sync.ListEvents(f.owner, &pb.ListEventsRequest{ChannelId: "general", After: boundary})
	if err != nil || len(events.GetEvents()) != 2 || events.Events[0].Type != "content.deleted" || events.Events[1].Content.Id != added.Content.Id {
		t.Fatalf("Граница событий: %v %v", events, err)
	}
	ids := map[string]bool{}
	for {
		for _, item := range page.Contents {
			if ids[item.Id] {
				t.Fatal("Повтор сообщения")
			}
			ids[item.Id] = true
		}
		if page.NextBeforeSequence == 0 {
			break
		}
		page, err = f.contents.GetHistoryPage(f.owner, &pb.GetHistoryPageRequest{ChannelId: "general", BeforeSequence: page.NextBeforeSequence})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(ids) != 1205 || !ids[first.Id] || ids[added.Content.Id] {
		t.Fatalf("Страницы потеряны/сдвинуты: %d", len(ids))
	}
	if !page.Contents[0].Deleted || page.Contents[0].Text != "" {
		t.Fatal("Удаление не учтено")
	}
}
func TestHistoryPageChecksAccessAndMode(t *testing.T) {
	f := newChannelFixture(t)
	page, err := f.contents.GetHistoryPage(f.owner, &pb.GetHistoryPageRequest{ChannelId: "general"})
	if err != nil || len(page.GetContents()) != 0 || page.NextBeforeSequence != 0 || page.EventCursor != "" {
		t.Fatalf("Пустая история: %v %v", page, err)
	}
	secret := f.create(t, "history_secret")
	member, principal := f.enroll(t, true)
	f.acl(t, secret, principalRule(principal, permissions(false, false, false, false)))
	_, err = f.contents.GetHistoryPage(member, &pb.GetHistoryPageRequest{ChannelId: secret.Id})
	requireCode(t, err, codes.NotFound)
	_, err = f.contents.GetHistoryPage(f.ctx, &pb.GetHistoryPageRequest{ChannelId: "general"})
	requireCode(t, err, codes.Unauthenticated)
	_, err = f.contents.GetHistoryPage(f.owner, &pb.GetHistoryPageRequest{ChannelId: "general", BeforeSequence: -1})
	requireCode(t, err, codes.InvalidArgument)
	alice, _ := enrollMls(t, f, true)
	_, err = f.channels.CreateChannel(alice, &pb.CreateChannelRequest{ChannelId: "history_mls", Title: "MLS", ViewType: "chat", MlsGroupId: bytes.Repeat([]byte{4}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.contents.GetHistoryPage(alice, &pb.GetHistoryPageRequest{ChannelId: "history_mls"})
	requireCode(t, err, codes.FailedPrecondition)
}
