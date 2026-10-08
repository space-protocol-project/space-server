package postgres

import (
	"crypto/ed25519"
	"crypto/rand"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestRecoveryAuthorityAndRevocation(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	store, identity, err := openManual(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth := NewAuth(store, identity.ServerID, "http://127.0.0.1:8080")
	root, rootPrivate, _ := ed25519.GenerateKey(rand.Reader)
	device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	prove := func(req *pb.CreateChallengeRequest, key ed25519.PrivateKey) *pb.CompleteChallengeResponse {
		t.Helper()
		challenge, err := auth.CreateChallenge(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		result, err := auth.CompleteChallenge(ctx, signedProof(t, challenge, key))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	original := prove(&pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device, Administrative: true}, rootPrivate)
	originalSession := prove(&pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: original.GrantId}, devicePrivate)
	recovery, recoveryPrivate, _ := ed25519.GenerateKey(rand.Reader)
	parent := prove(&pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: recovery, Administrative: true, Recovery: true}, rootPrivate)
	if parent.PrincipalId != original.PrincipalId {
		t.Fatal("Recovery создала другую идентичность")
	}
	duplicate, err := auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: recovery, Recovery: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.CompleteChallenge(ctx, signedProof(t, duplicate, rootPrivate)); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Второй recovery key", err)
	}
	restoredDevice, restoredPrivate, _ := ed25519.GenerateKey(rand.Reader)
	child := prove(&pb.CreateChallengeRequest{Purpose: "device.delegate", DevicePublicKey: restoredDevice, RecoveryGrantId: parent.GrantId, Administrative: true}, recoveryPrivate)
	childSession := prove(&pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: child.GrantId}, restoredPrivate)
	if id, err := store.Authenticate(ctx, childSession.AccessToken); err != nil || id != original.PrincipalId {
		t.Fatal(id, err)
	}
	if _, err = auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.delegate", DevicePublicKey: device, RecoveryGrantId: child.GrantId}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Повторная делегация рабочим ключом", err)
	}
	foreignRoot, foreignPrivate, _ := ed25519.GenerateKey(rand.Reader)
	foreign := prove(&pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: foreignRoot, DevicePublicKey: device}, foreignPrivate)
	if _, err = auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "recovery.device.revoke", RecoveryGrantId: parent.GrantId, GrantId: foreign.GrantId}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Отзыв чужой идентичности", err)
	}
	// Блокировка членства не снимается карточкой.
	if _, err = store.pool.Exec(ctx, "UPDATE memberships SET blocked=true WHERE principal_id=$1", original.PrincipalId); err != nil {
		t.Fatal(err)
	}
	if _, err = auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.delegate", DevicePublicKey: device, RecoveryGrantId: parent.GrantId}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Карточка обошла блокировку", err)
	}
	if _, err = store.pool.Exec(ctx, "UPDATE memberships SET blocked=false WHERE principal_id=$1", original.PrincipalId); err != nil {
		t.Fatal(err)
	}
	prove(&pb.CreateChallengeRequest{Purpose: "recovery.device.revoke", RecoveryGrantId: parent.GrantId, GrantId: original.GrantId}, recoveryPrivate)
	if _, err = store.Authenticate(ctx, originalSession.AccessToken); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Старая сессия пережила отзыв", err)
	}
	stale, err := auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.delegate", DevicePublicKey: device, RecoveryGrantId: parent.GrantId})
	if err != nil {
		t.Fatal(err)
	}
	prove(&pb.CreateChallengeRequest{Purpose: "device.revoke", RootPublicKey: root, GrantId: parent.GrantId}, rootPrivate)
	if _, err = store.Authenticate(ctx, childSession.AccessToken); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Дочерняя сессия пережила отзыв карточки", err)
	}
	if _, err = auth.CompleteChallenge(ctx, signedProof(t, stale, recoveryPrivate)); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Старый challenge пережил отзыв карточки", err)
	}
	// Непривилегированный recovery grant не может выдать space.manage.
	limited := prove(&pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: foreignRoot, DevicePublicKey: recovery, Recovery: true}, foreignPrivate)
	if _, err = auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.delegate", DevicePublicKey: device, RecoveryGrantId: limited.GrantId, Administrative: true}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Повышение device scopes", err)
	}
}
