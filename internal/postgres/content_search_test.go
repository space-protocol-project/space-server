package postgres

import (
	"bytes"
	"fmt"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"strings"
	"testing"
)

func TestSearchPagingLiteralQueryAndChanges(t *testing.T) {
	f := newChannelFixture(t)
	for i := 0; i < 55; i++ {
		_, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: fmt.Sprintf("Найти запись %02d", i), IdempotencyKey: fmt.Sprintf("search-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	first, err := f.contents.SearchContent(f.owner, &pb.SearchContentRequest{ChannelId: "general", Query: "нАйТи"})
	if err != nil || len(first.GetContents()) != 50 || first.NextBeforeSequence != 6 {
		t.Fatalf("Первая страница: %v %v", first, err)
	}
	second, err := f.contents.SearchContent(f.owner, &pb.SearchContentRequest{ChannelId: "general", Query: "нАйТи", BeforeSequence: first.NextBeforeSequence})
	if err != nil || len(second.GetContents()) != 5 || second.NextBeforeSequence != 0 || second.Contents[0].Sequence != 5 {
		t.Fatalf("Вторая страница: %v %v", second, err)
	}
	item := first.Contents[0]
	_, err = f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: item.Id, ExpectedRevision: item.Revision, Text: "Изменённый текст"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.contents.DeleteContent(f.owner, &pb.DeleteContentRequest{ChannelId: "general", Id: first.Contents[1].Id, ExpectedRevision: first.Contents[1].Revision})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := f.contents.SearchContent(f.owner, &pb.SearchContentRequest{ChannelId: "general", Query: "найти"})
	if err != nil || updated.Contents[0].Sequence != 53 {
		t.Fatalf("Удаление/правка: %v %v", updated, err)
	}
	for _, query := range []string{"%_", "' OR true --"} {
		page, err := f.contents.SearchContent(f.owner, &pb.SearchContentRequest{ChannelId: "general", Query: query})
		if err != nil || len(page.GetContents()) != 0 {
			t.Fatalf("Запрос не буквальный: %v %v", page, err)
		}
	}
}
func TestSearchRequiresSessionReadableOrdinaryChannel(t *testing.T) {
	f := newChannelFixture(t)
	secret := f.create(t, "search_secret")
	member, principal := f.enroll(t, true)
	f.acl(t, secret, principalRule(principal, permissions(false, false, false, false)))
	_, err := f.contents.SearchContent(member, &pb.SearchContentRequest{ChannelId: secret.Id, Query: "секрет"})
	requireCode(t, err, codes.NotFound)
	_, err = f.contents.SearchContent(f.ctx, &pb.SearchContentRequest{ChannelId: "general", Query: "секрет"})
	requireCode(t, err, codes.Unauthenticated)
	alice, _ := enrollMls(t, f, true)
	_, err = f.channels.CreateChannel(alice, &pb.CreateChannelRequest{ChannelId: "search_mls", Title: "MLS", ViewType: "chat", MlsGroupId: bytes.Repeat([]byte{12}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.contents.SearchContent(alice, &pb.SearchContentRequest{ChannelId: "search_mls", Query: "секрет"})
	requireCode(t, err, codes.FailedPrecondition)
	for _, query := range []string{"", "x", strings.Repeat("я", 129)} {
		_, err = f.contents.SearchContent(f.owner, &pb.SearchContentRequest{ChannelId: "general", Query: query})
		requireCode(t, err, codes.InvalidArgument)
	}
}

func TestSearchIndexUnicodeAndShortLiteral(t *testing.T) {
	f := newChannelFixture(t)
	texts := []string{"ЁЖ медведь", "你好世界", "😀🚀 запуск", "Aa%_", "abc xbcd", "real abcd"}
	for i, text := range texts {
		_, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: text, IdempotencyKey: fmt.Sprintf("unicode-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	for query, want := range map[string]string{"ёж": texts[0], "你好": texts[1], "😀🚀": texts[2], "%_": texts[3], "abcd": texts[5]} {
		result, err := f.contents.SearchContent(f.owner, &pb.SearchContentRequest{ChannelId: "general", Query: query})
		if err != nil || len(result.GetContents()) != 1 || result.Contents[0].Text != want {
			t.Fatalf("Буквальный запрос %q: %v %v", query, result, err)
		}
	}
}
