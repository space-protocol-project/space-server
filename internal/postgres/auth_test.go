package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"github.com/space-protocol-project/space-protocol/server/internal/chat"
	"github.com/space-protocol-project/space-protocol/server/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func complete(t *testing.T, ctx context.Context, client pb.AuthServiceClient, request *pb.CreateChallengeRequest, key ed25519.PrivateKey) *pb.CompleteChallengeResponse {
	t.Helper()
	challenge, err := client.CreateChallenge(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var transcript authn.Transcript
	if err = json.Unmarshal(challenge.Transcript, &transcript); err != nil {
		t.Fatal(err)
	}
	if transcript.Purpose != request.Purpose || transcript.Origin != "http://127.0.0.1:8080" || transcript.ChallengeID != challenge.ChallengeId {
		t.Fatal("Неверный transcript")
	}
	signing, err := transcript.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.CompleteChallenge(ctx, &pb.CompleteChallengeRequest{ChallengeId: challenge.ChallengeId, Signature: ed25519.Sign(key, signing)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAuthGatewayReplayEventsAndRevoke(t *testing.T) {
	ctx, databaseURL := isolatedDatabase(t)
	store, identity, err := openManual(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(authn.Interceptor(store)))
	auth := NewAuth(store, identity.ServerID, "http://127.0.0.1:8080")
	service := chat.NewPersistent(identity.ServerID, store)
	pb.RegisterAuthServiceServer(server, auth)
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
	handler, err := transport.Handler(ctx, connection, identity.ServerID, identity.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(handler)
	defer web.Close()
	authClient := pb.NewAuthServiceClient(connection)
	contents := pb.NewContentServiceClient(connection)
	syncClient := pb.NewSyncServiceClient(connection)
	root, rootPrivate, _ := ed25519.GenerateKey(rand.Reader)
	device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	registered := complete(t, ctx, authClient, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device}, rootPrivate)
	if registered.AccessToken != "" {
		t.Fatal("Root proof выдал токен без device proof")
	}
	challenge, err := authClient.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: registered.GrantId})
	if err != nil {
		t.Fatal(err)
	}
	var transcript authn.Transcript
	if err = json.Unmarshal(challenge.Transcript, &transcript); err != nil {
		t.Fatal(err)
	}
	signing, _ := transcript.SigningBytes()
	wrong := ed25519.Sign(rootPrivate, signing)
	if _, err = authClient.CompleteChallenge(ctx, &pb.CompleteChallengeRequest{ChallengeId: challenge.ChallengeId, Signature: wrong}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	altered := transcript
	altered.Origin = "http://127.0.0.1:9999"
	alteredSigning, _ := altered.SigningBytes()
	if _, err = authClient.CompleteChallenge(ctx, &pb.CompleteChallengeRequest{ChallengeId: challenge.ChallengeId, Signature: ed25519.Sign(devicePrivate, alteredSigning)}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	altered = transcript
	altered.ServerID = "srv_other"
	alteredSigning, _ = altered.SigningBytes()
	if _, err = authClient.CompleteChallenge(ctx, &pb.CompleteChallengeRequest{ChallengeId: challenge.ChallengeId, Signature: ed25519.Sign(devicePrivate, alteredSigning)}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	proof := &pb.CompleteChallengeRequest{ChallengeId: challenge.ChallengeId, Signature: ed25519.Sign(devicePrivate, signing)}
	var success atomic.Int32
	var workers sync.WaitGroup
	var session *pb.CompleteChallengeResponse
	var lock sync.Mutex
	for i := 0; i < 10; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			result, err := authClient.CompleteChallenge(ctx, proof)
			if err == nil {
				success.Add(1)
				lock.Lock()
				session = result
				lock.Unlock()
			} else if status.Code(err) != codes.Unauthenticated {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if success.Load() != 1 {
		t.Fatalf("Успешных потреблений challenge: %d", success.Load())
	}
	authorized := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+session.AccessToken)
	if _, err = contents.ListContent(ctx, &pb.ListContentRequest{ChannelId: "general"}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	request := &pb.CreateContentRequest{ChannelId: "general", Text: "Защищённое сообщение", IdempotencyKey: "same"}
	created, err := contents.CreateContent(authorized, request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Content.AuthorId != registered.PrincipalId {
		t.Fatal("Автор взят не из проверенной сессии")
	}
	if _, err = contents.CreateContent(authorized, request); err != nil {
		t.Fatal(err)
	}
	events, err := syncClient.ListEvents(authorized, &pb.ListEventsRequest{ChannelId: "general"})
	if err != nil || len(events.GetEvents()) != 1 {
		t.Fatalf("%v %v", events, err)
	}
	page, err := syncClient.ListEvents(authorized, &pb.ListEventsRequest{ChannelId: "general", After: events.NextCursor})
	if err != nil || len(page.GetEvents()) != 0 {
		t.Fatalf("%v %v", page, err)
	}
	for _, test := range []struct {
		authorization, spoof string
		expected             int
	}{{"", "", 401}, {"", session.AccessToken, 401}, {session.AccessToken, "", 200}} {
		req, _ := http.NewRequest("GET", web.URL+"/api/v1/channels/general/events", nil)
		if test.authorization != "" {
			req.Header.Set("Authorization", "Bearer "+test.authorization)
		}
		if test.spoof != "" {
			req.Header.Set("Grpc-Metadata-Authorization", "Bearer "+test.spoof)
			req.Header.Set("Grpc-Metadata-Actor", registered.PrincipalId)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != test.expected {
			t.Fatalf("HTTP %d вместо %d", response.StatusCode, test.expected)
		}
	}
	hash := sha256.Sum256([]byte(session.AccessToken))
	var stored []byte
	if err = store.pool.QueryRow(ctx, "SELECT token_hash FROM auth_sessions WHERE token_hash=$1", hash[:]).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(stored, []byte(session.AccessToken)) {
		t.Fatal("Токен хранится открыто")
	}
	// Ошибка записи события должна откатывать сообщение и счётчик.
	if _, err = store.pool.Exec(ctx, "ALTER TABLE events ADD CONSTRAINT test_failure CHECK(sequence<2)"); err != nil {
		t.Fatal(err)
	}
	secondRequest := &pb.CreateContentRequest{ChannelId: "general", Text: "Проверка rollback", IdempotencyKey: "rollback"}
	if _, err = contents.CreateContent(authorized, secondRequest); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	listed, err := contents.ListContent(authorized, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || len(listed.GetContents()) != 1 {
		t.Fatalf("Rollback: %v %v", listed, err)
	}
	if _, err = store.pool.Exec(ctx, "ALTER TABLE events DROP CONSTRAINT test_failure"); err != nil {
		t.Fatal(err)
	}
	second, err := contents.CreateContent(authorized, secondRequest)
	if err != nil || second.GetContent().GetId() != "message-2" {
		t.Fatalf("Счётчик после rollback: %v %v", second, err)
	}
	expired, err := authClient.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: registered.GrantId})
	if err != nil {
		t.Fatal(err)
	}
	var expiredTranscript authn.Transcript
	if err = json.Unmarshal(expired.Transcript, &expiredTranscript); err != nil {
		t.Fatal(err)
	}
	expiredSigning, _ := expiredTranscript.SigningBytes()
	if _, err = store.pool.Exec(ctx, "UPDATE auth_challenges SET expires_at=now()-interval '1 second' WHERE id=$1", expired.ChallengeId); err != nil {
		t.Fatal(err)
	}
	if _, err = authClient.CompleteChallenge(ctx, &pb.CompleteChallengeRequest{ChallengeId: expired.ChallengeId, Signature: ed25519.Sign(devicePrivate, expiredSigning)}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	otherRoot, otherRootPrivate, _ := ed25519.GenerateKey(rand.Reader)
	otherDevice, otherDevicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	otherGrant := complete(t, ctx, authClient, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: otherRoot, DevicePublicKey: otherDevice}, otherRootPrivate)
	otherSession := complete(t, ctx, authClient, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: otherGrant.GrantId}, otherDevicePrivate)
	otherContext := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+otherSession.AccessToken)
	otherMessage, err := contents.CreateContent(otherContext, request)
	if err != nil || otherMessage.GetContent().GetId() == created.Content.Id || otherMessage.GetContent().GetAuthorId() != otherGrant.PrincipalId {
		t.Fatalf("Ключи разных principals смешались: %v %v", otherMessage, err)
	}
	complete(t, ctx, authClient, &pb.CreateChallengeRequest{Purpose: "device.revoke", RootPublicKey: root, GrantId: registered.GrantId}, rootPrivate)
	if _, err = contents.ListContent(authorized, &pb.ListContentRequest{ChannelId: "general"}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	if _, err = authClient.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: registered.GrantId}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
}
