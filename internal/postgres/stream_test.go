package postgres

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"github.com/space-protocol-project/space-protocol/server/internal/chat"
	"github.com/space-protocol-project/space-protocol/server/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSubscribeReplayLiveHTTPAndRevoke(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	store, identity, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(authn.Interceptor(store)), grpc.StreamInterceptor(authn.StreamInterceptor(store)))
	service := chat.NewPersistent(identity.ServerID, store)
	pb.RegisterAuthServiceServer(server, NewAuth(store, identity.ServerID, "http://127.0.0.1:8080"))
	pb.RegisterContentServiceServer(server, service)
	pb.RegisterChannelServiceServer(server, service)
	pb.RegisterSyncServiceServer(server, store)
	go server.Serve(listener)
	defer server.Stop()
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	auth := pb.NewAuthServiceClient(connection)
	sync := pb.NewSyncServiceClient(connection)
	contents := pb.NewContentServiceClient(connection)
	root, rootPrivate, _ := ed25519.GenerateKey(rand.Reader)
	device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	grant := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device}, rootPrivate)
	session := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: grant.GrantId}, devicePrivate)
	authorized := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+session.AccessToken)
	unauthorized, err := sync.Subscribe(ctx, &pb.SubscribeRequest{ChannelId: "general"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = unauthorized.Recv(); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	first, err := contents.CreateContent(authorized, &pb.CreateContentRequest{ChannelId: "general", Text: "До подключения", IdempotencyKey: "first"})
	if err != nil {
		t.Fatal(err)
	}
	streamCtx, cancel := context.WithCancel(authorized)
	stream, err := sync.Subscribe(streamCtx, &pb.SubscribeRequest{ChannelId: "general"})
	if err != nil {
		t.Fatal(err)
	}
	hb, err := stream.Recv()
	if err != nil || !hb.GetHeartbeat() {
		t.Fatalf("%v %v", hb, err)
	}
	replay, err := stream.Recv()
	if err != nil || replay.GetEvent().GetContent().GetId() != first.Content.Id {
		t.Fatalf("%v %v", replay, err)
	}
	cursor := replay.Cursor
	cancel()
	second, err := contents.CreateContent(authorized, &pb.CreateContentRequest{ChannelId: "general", Text: "После разрыва", IdempotencyKey: "second"})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := sync.Subscribe(authorized, &pb.SubscribeRequest{ChannelId: "general", After: cursor})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resumed.Recv(); err != nil {
		t.Fatal(err)
	}
	next, err := resumed.Recv()
	if err != nil || next.GetEvent().GetContent().GetId() != second.Content.Id {
		t.Fatalf("%v %v", next, err)
	}
	handler, err := transport.Handler(ctx, connection, identity.ServerID, identity.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewUnstartedServer(handler)
	web.Config.WriteTimeout = 10 * time.Second
	web.Start()
	defer web.Close()
	request, _ := http.NewRequestWithContext(ctx, "GET", web.URL+"/api/v1/channels/general/events/subscribe?after="+next.Cursor, nil)
	request.Header.Set("Authorization", "Bearer "+session.AccessToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	decoder := json.NewDecoder(response.Body)
	var chunk struct {
		Result *pb.SubscribeResponse `json:"result"`
		Error  struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err = decoder.Decode(&chunk); err != nil || chunk.Result == nil || !chunk.Result.Heartbeat {
		t.Fatalf("%v %v", chunk, err)
	}
	// Heartbeat после 15 секунд проверяет, что HTTP WriteTimeout=10s продлён.
	chunk.Result = nil
	if err = decoder.Decode(&chunk); err != nil || chunk.Result == nil || !chunk.Result.Heartbeat {
		t.Fatalf("Heartbeat/flush: %v %v", chunk, err)
	}
	third, err := contents.CreateContent(authorized, &pb.CreateContentRequest{ChannelId: "general", Text: "Живое событие", IdempotencyKey: "third"})
	if err != nil {
		t.Fatal(err)
	}
	chunk.Result = nil
	if err = decoder.Decode(&chunk); err != nil || chunk.Result == nil || chunk.Result.Event.GetContent().GetId() != third.Content.Id {
		t.Fatalf("Live: %v %v", chunk, err)
	}
	complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.revoke", RootPublicKey: root, GrantId: grant.GrantId}, rootPrivate)
	// Native мог уже получить third; читаем до terminal отказа.
	for {
		_, err = resumed.Recv()
		if err != nil {
			break
		}
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	chunk.Result = nil
	if err = decoder.Decode(&chunk); err != nil || chunk.Error.Code != int(codes.Unauthenticated) {
		t.Fatalf("Terminal HTTP frame: %v %v", chunk, err)
	}
}
