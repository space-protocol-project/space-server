package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/chat"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func setup(t *testing.T) (context.Context, pb.ContentServiceClient, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	service := chat.New("test-server")
	pb.RegisterChannelServiceServer(server, service)
	pb.RegisterContentServiceServer(server, service)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	handler, err := Handler(ctx, connection, "test-server")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return ctx, pb.NewContentServiceClient(connection), httpServer.URL
}

func TestGatewayAndNativeShareState(t *testing.T) {
	ctx, client, origin := setup(t)
	response, err := http.Post(origin+"/api/v1/channels/general/content", "application/json", strings.NewReader(`{"text":"Привет","idempotencyKey":"request-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	created := new(pb.CreateContentResponse)
	if err := protojson.Unmarshal(body, created); err != nil {
		t.Fatal(err)
	}
	listed, err := client.ListContent(ctx, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || len(listed.GetContents()) != 1 || listed.Contents[0].Id != created.Content.Id {
		t.Fatalf("%v %v", listed, err)
	}
	repeated, err := client.CreateContent(ctx, &pb.CreateContentRequest{ChannelId: "general", Text: "Привет", IdempotencyKey: "request-1"})
	if err != nil || repeated.GetContent().GetId() != created.Content.Id {
		t.Fatalf("Повтор: %v %v", repeated, err)
	}
	_, err = client.CreateContent(ctx, &pb.CreateContentRequest{ChannelId: "general", Text: "Другой текст", IdempotencyKey: "request-1"})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatal(err)
	}
	empty, err := client.ListContent(ctx, &pb.ListContentRequest{ChannelId: "general", After: created.Content.Id})
	if err != nil || len(empty.GetContents()) != 0 {
		t.Fatalf("Курсор: %v %v", empty, err)
	}
	for _, path := range []string{"/.well-known/space-protocol", "/api/v1/manifest"} {
		r, err := http.Get(origin + path)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 || !strings.Contains(string(data), "test-server") {
			t.Fatalf("%s: %s", path, data)
		}
	}
	r, err := http.Get(origin + "/api/v1/channels/missing/content")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatal(r.StatusCode)
	}
}

func TestConcurrentRetryAndPagination(t *testing.T) {
	ctx, client, _ := setup(t)
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := client.CreateContent(ctx, &pb.CreateContentRequest{ChannelId: "general", Text: "Один запрос", IdempotencyKey: "same"})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	for i := 0; i < 104; i++ {
		_, err := client.CreateContent(ctx, &pb.CreateContentRequest{ChannelId: "general", Text: "Сообщение", IdempotencyKey: fmt.Sprint(i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	first, err := client.ListContent(ctx, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || len(first.GetContents()) != 100 {
		t.Fatalf("%v %v", first, err)
	}
	second, err := client.ListContent(ctx, &pb.ListContentRequest{ChannelId: "general", After: first.NextCursor})
	if err != nil || len(second.GetContents()) != 5 {
		t.Fatalf("%v %v", second, err)
	}
}

func TestRemovedFlutterPanelIsUnavailable(t *testing.T) {
	_, _, origin := setup(t)
	for _, path := range []string{"/space/flutter/", "/space/flutter/flutter_bootstrap.js"} {
		response, err := http.Get(origin + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d", path, response.StatusCode)
		}
	}
}

func TestRemovedPanelKeepsAPI(t *testing.T) {
	ctx := context.Background()
	connection, err := grpc.NewClient("127.0.0.1:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	handler, err := HandlerWithEndpoint(ctx, connection, "test-server", nil, "127.0.0.1:9090")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path   string
		status int
	}{{"/space", 404}, {"/space/", 404}, {"/space/identity.mjs", 404}, {"/healthz", 204}, {"/.well-known/space-protocol", 200}} {
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, httptest.NewRequest("GET", test.path, nil))
		if result.Code != test.status {
			t.Fatalf("%s: %d", test.path, result.Code)
		}
	}
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, httptest.NewRequest("GET", "/api/v1/manifest", nil))
	if result.Code == 404 {
		t.Fatal("API удалён вместе с панелью")
	}
}
