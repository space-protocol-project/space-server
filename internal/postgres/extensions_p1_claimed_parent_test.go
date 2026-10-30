package postgres

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/metadata"
)

func extensionClaimedParent(t *testing.T, f *channelFixture) extensionAuthorityFixture {
	t.Helper()
	root, private, _ := ed25519.GenerateKey(rand.Reader)
	sourceKey, sourcePrivate, _ := ed25519.GenerateKey(rand.Reader)
	sourceGrant := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: sourceKey}, private)
	login := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: sourceGrant.GrantId}, sourcePrivate)
	source := metadata.AppendToOutgoingContext(f.ctx, "authorization", "Bearer "+login.AccessToken)
	parentKey, parentPrivate, _ := ed25519.GenerateKey(rand.Reader)
	pair, err := f.auth.CreatePairing(f.ctx, &pb.CreatePairingRequest{PublicKey: parentKey, DeviceName: "P1 parent", Administrative: true, FullManagement: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.auth.ProposePairing(source, &pb.ProposePairingRequest{PairingId: pair.Pairing.Id}); err != nil {
		t.Fatal(err)
	}
	parent := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: parentKey, Administrative: true, FullManagement: true, PairingId: pair.Pairing.Id}, private)
	parentLogin := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: parent.GrantId}, parentPrivate)
	parentContext := metadata.AppendToOutgoingContext(f.ctx, "authorization", "Bearer "+parentLogin.AccessToken)
	if _, err = f.auth.ClaimPairing(parentContext, &pb.ClaimPairingRequest{PairingId: pair.Pairing.Id}); err != nil {
		t.Fatal(err)
	}
	childKey, childPrivate, _ := ed25519.GenerateKey(rand.Reader)
	child := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "device.delegate", RecoveryGrantId: parent.GrantId, DevicePublicKey: childKey, Administrative: true}, parentPrivate)
	childLogin := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: child.GrantId}, childPrivate)
	challenge, err := f.auth.CreateChallenge(f.ctx, &pb.CreateChallengeRequest{Purpose: "device.revoke", RootPublicKey: root, GrantId: parent.GrantId})
	if err != nil {
		t.Fatal(err)
	}
	proof := signedProof(t, challenge, private)
	return extensionAuthorityFixture{
		member: metadata.AppendToOutgoingContext(f.ctx, "authorization", "Bearer "+childLogin.AccessToken),
		grant:  child.GrantId, parent: parent.GrantId, pair: pair.Pairing.Id, lock: "SELECT g.id FROM device_grants",
		revoke: func() error { _, err := f.auth.CompleteChallenge(f.ctx, proof); return err },
	}
}
