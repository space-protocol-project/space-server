package postgres

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"github.com/space-protocol-project/space-protocol/server/internal/chat"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

func signedProof(t *testing.T, response *pb.CreateChallengeResponse, key ed25519.PrivateKey) *pb.CompleteChallengeRequest {
	t.Helper()
	var transcript authn.Transcript
	if err := json.Unmarshal(response.Transcript, &transcript); err != nil {
		t.Fatal(err)
	}
	signing, err := transcript.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	return &pb.CompleteChallengeRequest{ChallengeId: response.ChallengeId, Signature: ed25519.Sign(key, signing)}
}
func TestOwnerBootstrapScopesAndSettings(t *testing.T) {
	ctx, databaseURL := isolatedDatabase(t)
	store, identity, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(authn.Interceptor(store)), grpc.StreamInterceptor(authn.StreamInterceptor(store)))
	pb.RegisterAdminServiceServer(server, store)
	pb.RegisterAuthServiceServer(server, NewAuth(store, identity.ServerID, "http://127.0.0.1:8080"))
	pb.RegisterSyncServiceServer(server, store)
	service := chat.NewPersistent(identity.ServerID, store)
	pb.RegisterContentServiceServer(server, service)
	pb.RegisterChannelServiceServer(server, service)
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := pb.NewAuthServiceClient(conn)
	admin := pb.NewAdminServiceClient(conn)
	root, rootPrivate, _ := ed25519.GenerateKey(rand.Reader)
	device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	plain := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device}, rootPrivate)
	plainSession := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: plain.GrantId}, devicePrivate)
	plainContext := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+plainSession.AccessToken)
	if _, err = admin.GetSettings(ctx, &pb.GetSettingsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	oldCode, err := store.CreateSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	code, err := store.CreateSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.ClaimOwner(plainContext, &pb.ClaimOwnerRequest{SetupCode: code}); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	grant := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device, Administrative: true}, rootPrivate)
	session := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: grant.GrantId}, devicePrivate)
	ownerContext := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+session.AccessToken)
	if _, err = admin.GetSettings(ownerContext, &pb.GetSettingsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if _, err = admin.ClaimOwner(ownerContext, &pb.ClaimOwnerRequest{SetupCode: oldCode}); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	var count atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := admin.ClaimOwner(ownerContext, &pb.ClaimOwnerRequest{SetupCode: code})
			if err == nil {
				count.Add(1)
			} else if status.Code(err) != codes.PermissionDenied {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if count.Load() != 1 {
		t.Fatalf("Назначений владельца: %d", count.Load())
	}
	if _, err = store.CreateSetupCode(ctx); err == nil {
		t.Fatal("Код выдан после назначения владельца")
	}
	if _, err = admin.GetSettings(plainContext, &pb.GetSettingsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Обычный grant получил управление", err)
	}
	settings, err := admin.GetSettings(ownerContext, &pb.GetSettingsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	update := &pb.UpdateSettingsRequest{Title: "Наша мастерская", ChatTitle: "Разговоры", ChatEnabled: false, RegistrationPolicy: "closed", ExpectedRevision: settings.Settings.Revision}
	updated, err := admin.UpdateSettings(ownerContext, update)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.UpdateSettings(ownerContext, update); status.Code(err) != codes.Aborted {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(ctx, &pb.GetManifestRequest{})
	if err != nil || len(manifest.GetChannels()) != 0 {
		t.Fatalf("Manifest: %v %v", manifest, err)
	}
	if _, err = service.ListContent(ctx, &pb.ListContentRequest{ChannelId: "general"}); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	if _, err = store.ListEvents(ctx, &pb.ListEventsRequest{ChannelId: "general"}); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	update.ChatEnabled = true
	update.ExpectedRevision = updated.Settings.Revision
	if _, err = admin.UpdateSettings(ownerContext, update); err != nil {
		t.Fatal(err)
	}
	otherRoot, otherPrivate, _ := ed25519.GenerateKey(rand.Reader)
	otherDevice, _, _ := ed25519.GenerateKey(rand.Reader)
	challenge, err := auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: otherRoot, DevicePublicKey: otherDevice})
	if err != nil {
		t.Fatal(err)
	}
	// Подписываем корректно, чтобы отказ относился именно к policy, а не к подписи.
	if _, err = auth.CompleteChallenge(ctx, signedProof(t, challenge, otherPrivate)); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.revoke", RootPublicKey: root, GrantId: grant.GrantId}, rootPrivate)
	if _, err = admin.GetSettings(ownerContext, &pb.GetSettingsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
}
