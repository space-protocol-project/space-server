package postgres

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"

	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	rotation "github.com/space-protocol-project/space-protocol/server/internal/identityrotation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestRootRotationStablePrincipalAndRevocation(t *testing.T) {
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
	service := NewAuth(store, identity.ServerID, "http://127.0.0.1:8080")
	server := grpc.NewServer(grpc.UnaryInterceptor(authn.Interceptor(store)))
	pb.RegisterAuthServiceServer(server, service)
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := pb.NewAuthServiceClient(conn)
	root, oldPrivate, _ := ed25519.GenerateKey(rand.Reader)
	device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	original := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device, Administrative: true}, oldPrivate)
	if _, err = store.pool.Exec(ctx, "UPDATE memberships SET role='owner' WHERE principal_id=$1", original.PrincipalId); err != nil {
		t.Fatal(err)
	}
	login := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: original.GrantId}, devicePrivate)
	sourceCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+login.AccessToken)
	// Старый challenge и recovery должны стать недействительны после commit.
	stale, err := auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device})
	if err != nil {
		t.Fatal(err)
	}
	var staleT authn.Transcript
	json.Unmarshal(stale.Transcript, &staleT)
	staleBytes, _ := staleT.SigningBytes()
	recoveryPub, recoveryPrivate, _ := ed25519.GenerateKey(rand.Reader)
	parent := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: recoveryPub, Recovery: true}, oldPrivate)
	child, err := auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.delegate", RecoveryGrantId: parent.GrantId, DevicePublicKey: device})
	if err != nil {
		t.Fatal(err)
	}
	var childT authn.Transcript
	json.Unmarshal(child.Transcript, &childT)
	childBytes, _ := childT.SigningBytes()
	newRoot, newPrivate, _ := ed25519.GenerateKey(rand.Reader)
	newDevice, newDevicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	req := &pb.CreateRootRotationRequest{OperationId: "ro_" + strings.Repeat("a", 43), ExpectedAuthEpoch: 1, NewRootPublicKey: newRoot, NewDevicePublicKey: newDevice, Profile: "root-rotation-v1"}
	if _, err = auth.CreateRootRotation(ctx, req); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Гость создал ротацию", err)
	}
	challenge, err := auth.CreateRootRotation(sourceCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := auth.CreateRootRotation(sourceCtx, req)
	if err != nil || retry.ChallengeId != challenge.ChallengeId {
		t.Fatal("Повтор создания неидемпотентен", err)
	}
	var tr rotation.Transcript
	json.Unmarshal(challenge.Transcript, &tr)
	oldBytes, _ := tr.SigningBytes(false)
	newBytes, _ := tr.SigningBytes(true)
	proof := &pb.CompleteRootRotationRequest{ChallengeId: challenge.ChallengeId, OldSignature: ed25519.Sign(oldPrivate, oldBytes), NewSignature: ed25519.Sign(newPrivate, newBytes)}
	bad := &pb.CompleteRootRotationRequest{ChallengeId: proof.ChallengeId, OldSignature: proof.OldSignature, NewSignature: make([]byte, 64)}
	if _, err = auth.CompleteRootRotation(ctx, bad); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Неверная подпись принята", err)
	}
	// Параллельный повтор commit создаёт единственный grant.
	var results [2]*pb.CompleteRootRotationResponse
	var errs [2]error
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errs[i] = auth.CompleteRootRotation(ctx, proof) }(i)
	}
	wg.Wait()
	for _, err = range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	result := results[0]
	if result.PrincipalId != original.PrincipalId || result.AuthEpoch != 2 || result.GrantId != results[1].GrantId {
		t.Fatal("ID, эпоха или идемпотентность потеряны")
	}
	if _, err = store.Authenticate(ctx, login.AccessToken); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Прежняя сессия работает", err)
	}
	if _, err = auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Старый root зарегистрирован заново", err)
	}
	if _, err = auth.CompleteChallenge(ctx, &pb.CompleteChallengeRequest{ChallengeId: stale.ChallengeId, Signature: ed25519.Sign(oldPrivate, staleBytes)}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Старый challenge принят", err)
	}
	if _, err = auth.CompleteChallenge(ctx, &pb.CompleteChallengeRequest{ChallengeId: child.ChallengeId, Signature: ed25519.Sign(recoveryPrivate, childBytes)}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Старый recovery challenge принят", err)
	}
	fresh := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: result.GrantId}, newDevicePrivate)
	if fresh.PrincipalId != original.PrincipalId {
		t.Fatal("Новый вход изменил principal")
	}
	var role string
	if err = store.pool.QueryRow(ctx, "SELECT role FROM memberships WHERE principal_id=$1", result.PrincipalId).Scan(&role); err != nil || role != "owner" {
		t.Fatal("Роль потеряна", err)
	}
	again := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: newRoot, DevicePublicKey: newDevice}, newPrivate)
	if again.PrincipalId != original.PrincipalId {
		t.Fatal("Новый root создал другую идентичность")
	}
	store.Close()
	reopened, _, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.Authenticate(ctx, fresh.AccessToken); err != nil {
		t.Fatal("Новая сессия потеряна после reopen", err)
	}
}
