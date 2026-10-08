package postgres

import (
	"crypto/ed25519"
	"crypto/rand"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
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

func TestPairingApprovalClaimCancellationAndExpiry(t *testing.T) {
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
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := pb.NewAuthServiceClient(conn)
	root, rootPrivate, _ := ed25519.GenerateKey(rand.Reader)
	source, sourcePrivate, _ := ed25519.GenerateKey(rand.Reader)
	original := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: source}, rootPrivate)
	login := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: original.GrantId}, sourcePrivate)
	sourceContext := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+login.AccessToken)
	target, targetPrivate, _ := ed25519.GenerateKey(rand.Reader)
	start := func() *pb.CreatePairingResponse {
		t.Helper()
		p, err := auth.CreatePairing(ctx, &pb.CreatePairingRequest{PublicKey: target, DeviceName: "Новый компьютер", Administrative: true})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	prepare := func(p *pb.CreatePairingResponse) *pb.CreateChallengeResponse {
		t.Helper()
		if _, err := auth.ProposePairing(sourceContext, &pb.ProposePairingRequest{PairingId: p.Pairing.Id}); err != nil {
			t.Fatal(err)
		}
		req := &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: target, Administrative: true, PairingId: p.Pairing.Id}
		challenge, err := auth.CreateChallenge(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return challenge
	}
	p := start()
	if _, err = auth.ProposePairing(ctx, &pb.ProposePairingRequest{PairingId: p.Pairing.Id}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Анонимная root proposal", err)
	}
	if _, err = auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: target, Administrative: true, PairingId: p.Pairing.Id}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Подпись без предварительной сверки", err)
	}
	first := prepare(p)
	before, err := auth.PollPairing(ctx, &pb.PollPairingRequest{PollToken: p.PollToken})
	if err != nil || before.Pairing.State != "pending" || len(before.Signature) != 0 || string(before.RootPublicKey) != string(root) {
		t.Fatal(before, err)
	}
	if _, err = auth.PollPairing(ctx, &pb.PollPairingRequest{PollToken: p.Code}); status.Code(err) != codes.InvalidArgument {
		t.Fatal("Код заменил poll secret", err)
	}
	if _, err = auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: source, Administrative: true, PairingId: p.Pairing.Id}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Подмена целевого ключа", err)
	}
	if _, err = auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: target, PairingId: p.Pairing.Id}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Подмена scopes", err)
	}
	proofs := []*pb.CompleteChallengeRequest{signedProof(t, first, rootPrivate)}
	for i := 0; i < 3; i++ {
		proofs = append(proofs, signedProof(t, prepare(p), rootPrivate))
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for _, proof := range proofs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := auth.CompleteChallenge(ctx, proof); err == nil {
				wins.Add(1)
			} else if status.Code(err) != codes.FailedPrecondition {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("Сопряжение подтвердили %d раз", wins.Load())
	}
	approved, err := auth.PollPairing(ctx, &pb.PollPairingRequest{PollToken: p.PollToken})
	if err != nil || approved.Pairing.State != "approved" || len(approved.Signature) != 64 {
		t.Fatal(approved, err)
	}
	child := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: approved.GrantId}, targetPrivate)
	targetContext := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+child.AccessToken)
	if _, err = auth.ClaimPairing(sourceContext, &pb.ClaimPairingRequest{PairingId: p.Pairing.Id}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Чужое устройство завершило pairing", err)
	}
	if _, err = auth.ClaimPairing(targetContext, &pb.ClaimPairingRequest{PairingId: p.Pairing.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err = auth.ClaimPairing(targetContext, &pb.ClaimPairingRequest{PairingId: p.Pairing.Id}); err != nil {
		t.Fatal("Повтор claim", err)
	}
	if _, err = auth.PollPairing(ctx, &pb.PollPairingRequest{PollToken: p.PollToken}); status.Code(err) != codes.NotFound {
		t.Fatal("Poll после claim", err)
	}
	if _, err = auth.InspectPairing(ctx, &pb.InspectPairingRequest{Code: p.Code}); status.Code(err) != codes.NotFound {
		t.Fatal("Код после claim", err)
	}
	if _, err = store.pool.Exec(ctx, "UPDATE device_pairings SET expires_at=now()-interval '1 second' WHERE id=$1", p.Pairing.Id); err != nil {
		t.Fatal(err)
	}
	if id, err := store.Authenticate(ctx, child.AccessToken); err != nil || id != original.PrincipalId {
		t.Fatal("Завершённое pairing потеряло доступ", id, err)
	}
	cancelled := start()
	stale := prepare(cancelled)
	if _, err = auth.CancelPairing(ctx, &pb.CancelPairingRequest{PollToken: cancelled.PollToken}); err != nil {
		t.Fatal(err)
	}
	if _, err = auth.CompleteChallenge(ctx, signedProof(t, stale, rootPrivate)); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Подпись после отмены", err)
	}
	unclaimed := start()
	signed := prepare(unclaimed)
	if _, err = auth.CompleteChallenge(ctx, signedProof(t, signed, rootPrivate)); err != nil {
		t.Fatal(err)
	}
	result, err := auth.PollPairing(ctx, &pb.PollPairingRequest{PollToken: unclaimed.PollToken})
	if err != nil {
		t.Fatal(err)
	}
	temporary := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: result.GrantId}, targetPrivate)
	if _, err = store.pool.Exec(ctx, "UPDATE device_pairings SET expires_at=now()-interval '1 second' WHERE id=$1", unclaimed.Pairing.Id); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Authenticate(ctx, temporary.AccessToken); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Незавершённое pairing пережило срок", err)
	}
	start() // Очистка не должна сделать просроченный grant обычным разрешением.
	if _, err = store.Authenticate(ctx, temporary.AccessToken); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Очистка восстановила доступ", err)
	}
	var revoked bool
	if err = store.pool.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM device_grants WHERE id=$1", result.GrantId).Scan(&revoked); err != nil || !revoked {
		t.Fatal(revoked, err)
	}
	recovery, recoveryPrivate, _ := ed25519.GenerateKey(rand.Reader)
	parent := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: recovery, Administrative: true, Recovery: true}, rootPrivate)
	delegated := start()
	prepare(delegated)
	proof, err := auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.delegate", DevicePublicKey: target, Administrative: true, RecoveryGrantId: parent.GrantId, PairingId: delegated.Pairing.Id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.CompleteChallenge(ctx, signedProof(t, proof, recoveryPrivate)); err != nil {
		t.Fatal(err)
	}
	chain, err := auth.PollPairing(ctx, &pb.PollPairingRequest{PollToken: delegated.PollToken})
	if err != nil || len(chain.ParentSignature) != 64 || chain.ParentGrantId != parent.GrantId || len(chain.ParentTranscript) == 0 {
		t.Fatal("Цепочка recovery не выдана", chain, err)
	}
	restored := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: chain.GrantId}, targetPrivate)
	restoredContext := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+restored.AccessToken)
	if _, err = auth.ClaimPairing(restoredContext, &pb.ClaimPairingRequest{PairingId: delegated.Pairing.Id}); err != nil {
		t.Fatal(err)
	}
	complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "device.revoke", RootPublicKey: root, GrantId: parent.GrantId}, rootPrivate)
	if _, err = store.Authenticate(ctx, restored.AccessToken); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Pairing пережило отзыв recovery card", err)
	}
}
