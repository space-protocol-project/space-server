package postgres

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"sync"
	"testing"

	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"github.com/space-protocol-project/space-protocol/server/internal/chat"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestFirstSuccessfulLoginClaimsExactlyOneOwner(t *testing.T) {
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
	server := grpc.NewServer(grpc.UnaryInterceptor(authn.Interceptor(store)))
	pb.RegisterAuthServiceServer(server, NewAuth(store, identity.ServerID, "http://127.0.0.1:8080"))
	pb.RegisterAdminServiceServer(server, store)
	pb.RegisterMembershipServiceServer(server, store)
	pb.RegisterChannelServiceServer(server, chat.NewPersistent(identity.ServerID, store))
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth, admin, members := pb.NewAuthServiceClient(conn), pb.NewAdminServiceClient(conn), pb.NewMembershipServiceClient(conn)
	setup, err := admin.GetSetupStatus(ctx, &pb.GetSetupStatusRequest{})
	if err != nil || setup.Initialized || !setup.FirstLoginOwner {
		t.Fatal(setup, err)
	}
	// Чтение страниц и metadata не назначает владельца.
	if _, err = pb.NewChannelServiceClient(conn).GetManifest(ctx, &pb.GetManifestRequest{}); err != nil {
		t.Fatal(err)
	}
	const count = 8
	grants := make([]*pb.CompleteChallengeResponse, count)
	keys := make([]ed25519.PrivateKey, count)
	challenges := make([]*pb.CreateChallengeResponse, count)
	for i := 0; i < count; i++ {
		root, rootPrivate, _ := ed25519.GenerateKey(rand.Reader)
		device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
		grants[i] = complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device, Administrative: true}, rootPrivate)
		keys[i] = devicePrivate
		challenges[i], err = auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: grants[i].GrantId})
		if err != nil {
			t.Fatal(err)
		}
	}
	setup, err = admin.GetSetupStatus(ctx, &pb.GetSetupStatusRequest{})
	if err != nil || setup.Initialized {
		t.Fatal("Регистрация сама назначила владельца", setup, err)
	}
	proof := signedProof(t, challenges[0], keys[0])
	proof.Signature[0] ^= 1
	if _, err = auth.CompleteChallenge(ctx, proof); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	setup, _ = admin.GetSetupStatus(ctx, &pb.GetSetupStatusRequest{})
	if setup.Initialized {
		t.Fatal("Неверная подпись назначила владельца")
	}
	// Блокировка также исключает автоматическое назначение.
	if _, err = store.pool.Exec(ctx, "UPDATE memberships SET blocked=true WHERE principal_id=$1", grants[0].PrincipalId); err != nil {
		t.Fatal(err)
	}
	if _, err = auth.CompleteChallenge(ctx, signedProof(t, challenges[0], keys[0])); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, "UPDATE memberships SET blocked=false WHERE principal_id=$1", grants[0].PrincipalId); err != nil {
		t.Fatal(err)
	}
	sessions := make([]*pb.CompleteChallengeResponse, count)
	proofs := make([]*pb.CompleteChallengeRequest, count)
	for i := range proofs {
		proofs[i] = signedProof(t, challenges[i], keys[i])
	}
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			sessions[i], err = auth.CompleteChallenge(ctx, proofs[i])
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	ownerCount := 0
	var owner string
	for i, session := range sessions {
		if session == nil {
			t.Fatal("Вход не завершён")
		}
		authorized := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+session.AccessToken)
		m, err := members.GetMembership(authorized, &pb.GetMembershipRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if m.Member.Role == "owner" {
			ownerCount++
			owner = grants[i].PrincipalId
			if _, err = admin.GetSettings(authorized, &pb.GetSettingsRequest{}); err != nil {
				t.Fatal(err)
			}
		}
		if m.Member.Role != "owner" {
			if _, err = admin.GetSettings(authorized, &pb.GetSettingsRequest{}); status.Code(err) != codes.PermissionDenied {
				t.Fatal(err)
			}
		}
	}
	if ownerCount != 1 {
		t.Fatal(ownerCount)
	}
	var audits int
	if err = store.pool.QueryRow(ctx, "SELECT count(*) FROM admin_audit WHERE action='owner.first-login'").Scan(&audits); err != nil || audits != 1 {
		t.Fatal(audits, err)
	}
	if _, err = store.CreateSetupCode(ctx); err == nil {
		t.Fatal("Назначенного владельца можно заменить")
	}
	if err = store.EnableFirstOwner(ctx); err == nil {
		t.Fatal("Автовыбор повторно открыт")
	}
	reopened, id, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var saved string
	if err = reopened.pool.QueryRow(ctx, "SELECT owner_id FROM space_settings").Scan(&saved); err != nil || saved != owner || id.ServerID != identity.ServerID {
		t.Fatal(saved, err)
	}
}

func TestSetupCodeExplicitlyKeepsManualBootstrap(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	store, _, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.CreateSetupCode(ctx); err != nil {
		t.Fatal(err)
	}
	setup, err := store.GetSetupStatus(ctx, &pb.GetSetupStatusRequest{})
	if err != nil || setup.FirstLoginOwner || setup.Initialized {
		t.Fatal(setup, err)
	}
	if err = store.EnableFirstOwner(ctx); err != nil {
		t.Fatal(err)
	}
	var hash []byte
	if err = store.pool.QueryRow(ctx, "SELECT setup_code_hash FROM space_settings").Scan(&hash); err != nil || hash != nil {
		t.Fatal(hash, err)
	}
}
