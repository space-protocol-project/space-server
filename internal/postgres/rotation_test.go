package postgres

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"

	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"github.com/space-protocol-project/space-protocol/server/internal/chat"
	rotation "github.com/space-protocol-project/space-protocol/server/internal/identityrotation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestRootRotationStablePrincipalAndRevocation(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	store, identity, err := openManual(ctx, url)
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
	pb.RegisterContentServiceServer(server, chat.NewPersistent(identity.ServerID, store))
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
	if _, err = store.pool.Exec(ctx, "UPDATE space_settings SET owner_id=$1 WHERE singleton=true", original.PrincipalId); err != nil {
		t.Fatal(err)
	}
	login := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: original.GrantId}, devicePrivate)
	sourceCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+login.AccessToken)
	contents := pb.NewContentServiceClient(conn)
	message, err := contents.CreateContent(sourceCtx, &pb.CreateContentRequest{ChannelId: "general", Text: "До смены ключа", IdempotencyKey: "rotation-history"})
	if err != nil {
		t.Fatal(err)
	}
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
	pair, err := auth.CreatePairing(ctx, &pb.CreatePairingRequest{PublicKey: newDevice, DeviceName: "Незавершённый запрос"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.ProposePairing(sourceCtx, &pb.ProposePairingRequest{PairingId: pair.Pairing.Id}); err != nil {
		t.Fatal(err)
	}
	req := &pb.CreateRootRotationRequest{OperationId: "ro_" + strings.Repeat("a", 43), ExpectedAuthEpoch: 1, NewRootPublicKey: newRoot, NewDevicePublicKey: newDevice, Profile: "root-rotation-v1"}
	reuse := &pb.CreateRootRotationRequest{OperationId: "ro_" + strings.Repeat("b", 43), ExpectedAuthEpoch: 1, NewRootPublicKey: newRoot, NewDevicePublicKey: device, Profile: "root-rotation-v1"}
	if _, err = auth.CreateRootRotation(sourceCtx, reuse); status.Code(err) != codes.AlreadyExists {
		t.Fatal("Прежний device key принят как новый", err)
	}
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
	// Эмуляция истечения persisted challenge: обновление сохраняет operation/keys.
	if _, err = store.pool.Exec(ctx, "UPDATE root_rotations SET expires_at=now()-interval '1 second' WHERE id=$1", challenge.ChallengeId); err != nil {
		t.Fatal(err)
	}
	renewed, err := auth.CreateRootRotation(sourceCtx, req)
	if err != nil || renewed.ChallengeId == challenge.ChallengeId {
		t.Fatal("Истёкший запрос не обновлён", err)
	}
	var renewedProof rotation.Transcript
	if err = json.Unmarshal(renewed.Transcript, &renewedProof); err != nil {
		t.Fatal(err)
	}
	if renewedProof.OperationID != req.OperationId || renewedProof.NewRootPublicKey != base64.RawURLEncoding.EncodeToString(newRoot) || renewedProof.NewDevicePublicKey != base64.RawURLEncoding.EncodeToString(newDevice) {
		t.Fatal("Обновление сменило operation или новые ключи")
	}
	challenge = renewed
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
	var pairState string
	if err = store.pool.QueryRow(ctx, "SELECT state FROM device_pairings WHERE id=$1", pair.Pairing.Id).Scan(&pairState); err != nil || pairState != "cancelled" {
		t.Fatal("Прежний pairing не закрыт", err)
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
	member, err := membership(ctx, store.pool, result.PrincipalId, false)
	if err != nil || member.Role != "owner" {
		t.Fatal("Роль потеряна", err)
	}
	newContext := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+fresh.AccessToken)
	history, err := contents.ListContent(newContext, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || len(history.Contents) != 1 || history.Contents[0].Id != message.Content.Id || history.Contents[0].AuthorId != original.PrincipalId {
		t.Fatal("История или авторство потеряны", err)
	}
	again := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: newRoot, DevicePublicKey: newDevice}, newPrivate)
	if again.PrincipalId != original.PrincipalId {
		t.Fatal("Новый root создал другую идентичность")
	}
	store.Close()
	reopened, _, err := openManual(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.Authenticate(ctx, fresh.AccessToken); err != nil {
		t.Fatal("Новая сессия потеряна после reopen", err)
	}
}
