package chat

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"testing"
	"time"
)

func TestCreationTimeSurvivesReplayAndHistory(t *testing.T) {
	service := New("srv_test")
	request := &pb.CreateContentRequest{ChannelId: "general", Text: "Текст", IdempotencyKey: "request-1"}
	before := time.Now().UnixMilli()
	first, err := service.CreateContent(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Content.CreatedAtUnixMs < before || first.Content.CreatedAtUnixMs > time.Now().UnixMilli() {
		t.Fatal("Время вне интервала создания")
	}
	repeated, err := service.CreateContent(context.Background(), request)
	if err != nil || repeated.Content.CreatedAtUnixMs != first.Content.CreatedAtUnixMs {
		t.Fatalf("Повтор изменил время: %v", err)
	}
	history, err := service.ListContent(context.Background(), &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || len(history.Contents) != 1 || history.Contents[0].CreatedAtUnixMs != first.Content.CreatedAtUnixMs {
		t.Fatalf("История изменила время: %v", err)
	}
}
