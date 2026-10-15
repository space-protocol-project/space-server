package postgres

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rotation "github.com/space-protocol-project/space-server/internal/identityrotation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func TestRootRetentionRechecksAuthorityAndExpiry(t *testing.T) {
	for _, change := range []string{"none", "revoked", "parent", "scopes", "expiry", "expired"} {
		t.Run(change, func(t *testing.T) {
			f := newChannelFixture(t)
			root, private, _ := ed25519.GenerateKey(rand.Reader)
			device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
			source := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device}, private)
			session := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: source.GrantId}, devicePrivate)
			actor := metadata.AppendToOutgoingContext(f.ctx, "authorization", "Bearer "+session.AccessToken)
			recovery, recoveryPrivate, _ := ed25519.GenerateKey(rand.Reader)
			parent := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: recovery, Recovery: true}, private)
			childKey, childPrivate, _ := ed25519.GenerateKey(rand.Reader)
			child := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "device.delegate", RecoveryGrantId: parent.GrantId, DevicePublicKey: childKey}, recoveryPrivate)
			oldSession := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: child.GrantId}, childPrivate)
			tx, err := f.store.pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			devices, err := retentionSnapshot(f.ctx, tx, source.PrincipalId, []string{child.GrantId})
			tx.Rollback(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := rotation.RetentionRoot(devices)
			if err != nil {
				t.Fatal(err)
			}
			nextRoot, nextPrivate, _ := ed25519.GenerateKey(rand.Reader)
			nextDevice, _, _ := ed25519.GenerateKey(rand.Reader)
			operation, _ := randomString("ro_")
			req := &pb.CreateRootRotationRequest{Profile: "root-rotation-v2", OperationId: operation, ExpectedAuthEpoch: 1, NewRootPublicKey: nextRoot, NewDevicePublicKey: nextDevice, RetainedGrantIds: []string{child.GrantId}, RetainedDevicesHash: hash}
			challenge, err := f.auth.CreateRootRotation(actor, req)
			if err != nil {
				t.Fatal(err)
			}
			var transcript rotation.Transcript
			if json.Unmarshal(challenge.Transcript, &transcript) != nil {
				t.Fatal("Неверный transcript")
			}
			oldBytes, _ := transcript.SigningBytes(false)
			newBytes, _ := transcript.SigningBytes(true)
			proof := &pb.CompleteRootRotationRequest{ChallengeId: challenge.ChallengeId, OldSignature: ed25519.Sign(private, oldBytes), NewSignature: ed25519.Sign(nextPrivate, newBytes)}
			queries := map[string]string{
				"revoked": "UPDATE device_grants SET revoked_at=now() WHERE id=$1",
				"parent":  "UPDATE device_grants SET revoked_at=now() WHERE id=$1",
				"scopes":  "UPDATE device_grants SET scopes=ARRAY['chat.read','chat.write','space.manage'] WHERE id=$1",
				"expired": "UPDATE device_grants SET expires_at=now()-interval '1 second' WHERE id=$1",
				"expiry":  "UPDATE device_grants SET expires_at=expires_at+interval '1 day' WHERE id=$1",
			}
			if change != "none" {
				id := child.GrantId
				if change == "parent" {
					id = parent.GrantId
				}
				if _, err = f.store.pool.Exec(f.ctx, queries[change], id); err != nil {
					t.Fatal(err)
				}
				_, err = f.auth.CompleteRootRotation(f.ctx, proof)
				requireCode(t, err, codes.FailedPrecondition)
				var epoch int64
				if err = f.store.pool.QueryRow(f.ctx, "SELECT auth_epoch FROM principals WHERE id=$1", source.PrincipalId).Scan(&epoch); err != nil || epoch != 1 {
					t.Fatal("Частичная смена root", err)
				}
				return
			}
			if _, err = f.auth.CompleteRootRotation(f.ctx, proof); err != nil {
				t.Fatal(err)
			}
			_, err = f.store.Authenticate(f.ctx, oldSession.AccessToken)
			requireCode(t, err, codes.Unauthenticated)
			fresh := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: child.GrantId}, childPrivate)
			if fresh.PrincipalId != source.PrincipalId {
				t.Fatal("Личность изменилась")
			}
			login, err := f.auth.CreateChallenge(f.ctx, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: child.GrantId})
			if err != nil || login.RetentionProof == nil || len(login.RootHistory) != 1 {
				t.Fatal("Нет доказательств для автономного устройства", err)
			}
			p := login.RetentionProof
			if rotation.VerifyRetention(hash, rotation.RetentionProof{Device: devices[0], Index: int(p.Index), Count: int(p.Count), Siblings: p.Siblings}) != nil {
				t.Fatal("Недействительное доказательство")
			}
			var parentID *string
			var expiry int64
			if err = f.store.pool.QueryRow(f.ctx, "SELECT parent_grant_id,extract(epoch FROM expires_at)::bigint FROM device_grants WHERE id=$1", child.GrantId).Scan(&parentID, &expiry); err != nil || parentID != nil || expiry != devices[0].ExpiresAt {
				t.Fatal("Права не сохранены", err)
			}
			_, err = f.auth.CreateChallenge(f.ctx, &pb.CreateChallengeRequest{Purpose: "device.delegate", RecoveryGrantId: parent.GrantId, DevicePublicKey: device})
			requireCode(t, err, codes.Unauthenticated)
		})
	}
}
