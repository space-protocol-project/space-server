package postgres

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rotation "github.com/space-protocol-project/space-server/internal/identityrotation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

type extensionAuthorityFixture struct {
	member context.Context
	revoke func() error
	lock   string
}

// Настоящие подписанные регистрации, делегирование, pairing и RPC отзыва.
func extensionAuthority(t *testing.T, f *channelFixture, kind string) extensionAuthorityFixture {
	t.Helper()
	root, private, _ := ed25519.GenerateKey(rand.Reader)
	device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	original := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device, Administrative: true}, private)
	login := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: original.GrantId}, devicePrivate)
	source := metadata.AppendToOutgoingContext(f.ctx, "authorization", "Bearer "+login.AccessToken)
	recovery, recoveryPrivate, _ := ed25519.GenerateKey(rand.Reader)
	parent := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: recovery, Administrative: true, Recovery: true}, private)
	child, childPrivate, _ := ed25519.GenerateKey(rand.Reader)
	pair, err := f.auth.CreatePairing(f.ctx, &pb.CreatePairingRequest{PublicKey: child, DeviceName: "P1", Administrative: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.auth.ProposePairing(source, &pb.ProposePairingRequest{PairingId: pair.Pairing.Id}); err != nil {
		t.Fatal(err)
	}
	grant := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "device.delegate", RecoveryGrantId: parent.GrantId, DevicePublicKey: child, Administrative: true, PairingId: pair.Pairing.Id}, recoveryPrivate)
	session := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: grant.GrantId}, childPrivate)
	result := extensionAuthorityFixture{member: metadata.AppendToOutgoingContext(f.ctx, "authorization", "Bearer "+session.AccessToken)}
	switch kind {
	case "pairing":
		result.lock = "FROM device_pairings"
		result.revoke = func() error {
			_, err := f.auth.CancelPairing(f.ctx, &pb.CancelPairingRequest{PollToken: pair.PollToken})
			return err
		}
	case "parent":
		challenge, err := f.auth.CreateChallenge(f.ctx, &pb.CreateChallengeRequest{Purpose: "device.revoke", RootPublicKey: root, GrantId: parent.GrantId})
		if err != nil {
			t.Fatal(err)
		}
		proof := signedProof(t, challenge, private)
		result.lock = "SELECT g.id FROM device_grants"
		result.revoke = func() error { _, err := f.auth.CompleteChallenge(f.ctx, proof); return err }
	case "rotation":
		result.lock = "FROM device_pairings"
		result.revoke = extensionRotation(t, f, source, private)
	default:
		t.Fatal("Неизвестный сценарий", kind)
	}
	return result
}

func extensionRotation(t *testing.T, f *channelFixture, source context.Context, private ed25519.PrivateKey) func() error {
	t.Helper()
	root, nextPrivate, _ := ed25519.GenerateKey(rand.Reader)
	device, _, _ := ed25519.GenerateKey(rand.Reader)
	challenge, err := f.auth.CreateRootRotation(source, &pb.CreateRootRotationRequest{OperationId: "ro_" + strings.Repeat("a", 43), ExpectedAuthEpoch: 1, NewRootPublicKey: root, NewDevicePublicKey: device, Profile: "root-rotation-v1"})
	if err != nil {
		t.Fatal(err)
	}
	var transcript rotation.Transcript
	if err = json.Unmarshal(challenge.Transcript, &transcript); err != nil {
		t.Fatal(err)
	}
	oldBytes, err := transcript.SigningBytes(false)
	if err != nil {
		t.Fatal(err)
	}
	newBytes, err := transcript.SigningBytes(true)
	if err != nil {
		t.Fatal(err)
	}
	proof := &pb.CompleteRootRotationRequest{ChallengeId: challenge.ChallengeId, OldSignature: ed25519.Sign(private, oldBytes), NewSignature: ed25519.Sign(nextPrivate, newBytes)}
	return func() error { _, err := f.auth.CompleteRootRotation(f.ctx, proof); return err }
}

func TestExtensionP1MutationAuthorityBeforeCommit(t *testing.T) {
	for _, kind := range []string{"parent", "pairing", "rotation"} {
		t.Run(kind, func(t *testing.T) {
			f := newChannelFixture(t)
			c := f.create(t, "mut_authority")
			f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
			a := extensionAuthority(t, f, kind)
			h := &mutationProbe{run: func(context.Context) ([]byte, error) {
				if err := a.revoke(); err != nil {
					return nil, err
				}
				return mutationOutput(0), nil
			}}
			api := mutationClient(t, f, h, []byte{1}, c.Id)
			_, err := api.ExecuteExtensionCommand(a.member, mutationRequest("authority-before-001", 0))
			requireCode(t, err, codes.PermissionDenied)
			mutationCounts(t, f, 0, 0, 0, 0)
		})
	}
}

func TestExtensionP1MutationAuthorityAfterCommit(t *testing.T) {
	for _, kind := range []string{"parent", "pairing", "rotation"} {
		t.Run(kind, func(t *testing.T) {
			f := newChannelFixture(t)
			c := f.create(t, "mut_authority")
			f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
			a := extensionAuthority(t, f, kind)
			h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
			api := mutationClient(t, f, h, []byte{1}, c.Id)
			release := extensionCommitBarrier(t, f)
			done := make(chan error, 1)
			request := mutationRequest("authority-after-001", 0)
			go func() { _, err := api.ExecuteExtensionCommand(a.member, request); done <- err }()
			waitExtensionSQLLock(t, f, "INSERT INTO extension_p1_outbox")
			revoked := make(chan error, 1)
			go func() { revoked <- a.revoke() }()
			waitExtensionSQLLock(t, f, a.lock)
			release()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := <-revoked; err != nil {
				t.Fatal(err)
			}
			mutationCounts(t, f, 1, 1, 1, 1)
			_, err := api.ExecuteExtensionCommand(a.member, request)
			requireCode(t, err, codes.Unauthenticated)
		})
	}
}

func extensionCommitBarrier(t *testing.T, f *channelFixture) func() {
	t.Helper()
	gate, err := f.store.pool.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.Release)
	if _, err = gate.Exec(f.ctx, "SELECT pg_advisory_lock(974422)"); err != nil {
		t.Fatal(err)
	}
	release := func() {
		_, err := gate.Exec(context.Background(), "SELECT pg_advisory_unlock(974422)")
		if err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(release)
	_, err = f.store.pool.Exec(f.ctx, `CREATE FUNCTION extension_p1_wait_commit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_advisory_xact_lock(974422); RETURN NEW; END$$;
 CREATE TRIGGER wait_commit BEFORE INSERT ON extension_p1_outbox FOR EACH ROW EXECUTE FUNCTION extension_p1_wait_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	return release
}
