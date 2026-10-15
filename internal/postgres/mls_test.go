package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

func enrollMls(t *testing.T, f *channelFixture, admin bool) (context.Context, *pb.MlsDevice) {
	t.Helper()
	root, rootPrivate, _ := ed25519.GenerateKey(rand.Reader)
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	grant := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: public, Administrative: admin}, rootPrivate)
	login := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: grant.GrantId}, private)
	ctx := metadata.AppendToOutgoingContext(f.ctx, "authorization", "Bearer "+login.AccessToken)
	if admin {
		_, err := f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: grant.PrincipalId, Role: "admin", ExpectedRevision: 1})
		if err != nil {
			t.Fatal(err)
		}
	}
	mlsPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	binding := mlsBinding{Device: mlsScope{ServerID: f.identity.ServerID, PrincipalID: grant.PrincipalId, GrantID: grant.GrantId}}
	copy(binding.Device.PublicKey[:], public)
	copy(binding.PublicKey[:], mlsPublic)
	signing, _ := json.Marshal([]any{"space/mls/binding/v1", binding.Device, binding.PublicKey})
	copy(binding.Signature[:], ed25519.Sign(private, signing))
	raw, _ := json.Marshal(binding)
	registered, err := pb.NewMlsServiceClient(f.conn).RegisterMlsDevice(ctx, &pb.RegisterMlsDeviceRequest{Binding: raw, KeyPackage: []byte("opaque-key-package")})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, registered.Device
}

func TestMlsDeliveryOrderMembershipAndPlaintextRejection(t *testing.T) {
	f := newChannelFixture(t)
	alice, a := enrollMls(t, f, true)
	bob, b := enrollMls(t, f, false)
	outsider, _ := enrollMls(t, f, false)
	client := pb.NewMlsServiceClient(f.conn)
	group := bytes.Repeat([]byte{42}, 32)
	channel, err := f.channels.CreateChannel(alice, &pb.CreateChannelRequest{ChannelId: "private", Title: "Закрытый", ViewType: "chat", MlsGroupId: group})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(channel.Channel.MlsGroupId, group) {
		t.Fatal("Режим не сохранён")
	}
	_, err = f.contents.CreateContent(alice, &pb.CreateContentRequest{ChannelId: "private", Text: "plaintext", IdempotencyKey: "bad"})
	requireCode(t, err, codes.FailedPrecondition)
	_, err = f.contents.ListContent(alice, &pb.ListContentRequest{ChannelId: "private"})
	requireCode(t, err, codes.FailedPrecondition)
	request := &pb.PublishMlsRequest{ChannelId: "private", RequestId: bytes.Repeat([]byte{1}, 16), Kind: "commit", Ciphertext: []byte("commit"), AddGrantId: b.GrantId, Welcome: []byte("welcome")}
	first, err := client.PublishMls(alice, request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := client.PublishMls(alice, request)
	if err != nil || !proto.Equal(first, again) {
		t.Fatal("Повтор Commit изменил ответ", err)
	}
	request.Ciphertext = []byte("changed")
	_, err = client.PublishMls(alice, request)
	requireCode(t, err, codes.AlreadyExists)
	history, err := client.ListMlsEnvelopes(bob, &pb.ListMlsEnvelopesRequest{ChannelId: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Envelopes) != 1 || string(history.Envelopes[0].Welcome) != "welcome" || len(history.Envelopes[0].Roster) != 2 {
		t.Fatal("Не доставлен Welcome и состав")
	}
	ownerHistory, err := client.ListMlsEnvelopes(alice, &pb.ListMlsEnvelopesRequest{ChannelId: "private"})
	if err != nil || len(ownerHistory.Envelopes[0].Welcome) != 0 {
		t.Fatal("Welcome выдан не получателю", err)
	}
	_, err = client.ListMlsEnvelopes(outsider, &pb.ListMlsEnvelopesRequest{ChannelId: "private"})
	requireCode(t, err, codes.PermissionDenied)
	message := &pb.PublishMlsRequest{ChannelId: "private", RequestId: bytes.Repeat([]byte{2}, 16), ExpectedEpoch: 0, Kind: "application", Ciphertext: []byte("ciphertext")}
	_, err = client.PublishMls(bob, message)
	requireCode(t, err, codes.Aborted)
	message.ExpectedEpoch = 1
	if _, err = client.PublishMls(bob, message); err != nil {
		t.Fatal(err)
	}
	request = &pb.PublishMlsRequest{ChannelId: "private", RequestId: bytes.Repeat([]byte{3}, 16), ExpectedEpoch: 1, Kind: "commit", Ciphertext: []byte("remove"), RemoveGrantId: b.GrantId}
	if _, err = client.PublishMls(alice, request); err != nil {
		t.Fatal(err)
	}
	_, err = client.ListMlsEnvelopes(bob, &pb.ListMlsEnvelopesRequest{ChannelId: "private"})
	requireCode(t, err, codes.PermissionDenied)
	current, err := client.GetMlsChannel(alice, &pb.GetMlsChannelRequest{ChannelId: "private"})
	if err != nil || current.Epoch != 2 || len(current.Devices) != 1 || current.Devices[0].GrantId != a.GrantId {
		t.Fatal(current, err)
	}
}

func TestMlsFilesKeepOpaqueNamesAndCannotBeCancelledAfterPublish(t *testing.T) {
	f := newChannelFixture(t)
	alice, _ := enrollMls(t, f, true)
	_, err := f.channels.CreateChannel(alice, &pb.CreateChannelRequest{ChannelId: "files", Title: "Закрытый", ViewType: "chat", MlsGroupId: bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	files := pb.NewAssetServiceClient(f.conn)
	blob := bytes.Repeat([]byte{0x42}, 128)
	hash := sha256.Sum256(blob)
	req := &pb.BeginUploadRequest{ChannelId: "files", Name: "secret-name.txt", ByteSize: int64(len(blob)), Sha256: hash[:], IdempotencyKey: "file", Encrypted: true}
	_, err = files.BeginUpload(alice, req)
	requireCode(t, err, codes.InvalidArgument)
	req.Name = "encrypted-file"
	upload, err := files.BeginUpload(alice, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = files.UploadChunk(alice, &pb.UploadChunkRequest{AssetId: upload.Asset.Id, Data: blob}); err != nil {
		t.Fatal(err)
	}
	if _, err = files.CompleteUpload(alice, &pb.CompleteUploadRequest{AssetId: upload.Asset.Id}); err != nil {
		t.Fatal(err)
	}
	publish := &pb.PublishMlsRequest{ChannelId: "files", RequestId: bytes.Repeat([]byte{1}, 16), Kind: "application", Ciphertext: []byte("encrypted-metadata"), AttachmentIds: []string{upload.Asset.Id}}
	if _, err = pb.NewMlsServiceClient(f.conn).PublishMls(alice, publish); err != nil {
		t.Fatal(err)
	}
	downloaded, err := files.DownloadChunk(alice, &pb.DownloadChunkRequest{AssetId: upload.Asset.Id})
	if err != nil || !bytes.Equal(downloaded.Data, blob) {
		t.Fatal(downloaded, err)
	}
	_, err = files.CancelUpload(alice, &pb.CancelUploadRequest{AssetId: upload.Asset.Id})
	requireCode(t, err, codes.FailedPrecondition)
	_, err = files.ListMyAssets(alice, &pb.ListMyAssetsRequest{ChannelId: "files"})
	requireCode(t, err, codes.FailedPrecondition)
}

func TestMlsRevokedDeviceMustBeRemovedBeforeSending(t *testing.T) {
	f := newChannelFixture(t)
	alice, _ := enrollMls(t, f, true)
	_, bob := enrollMls(t, f, false)
	_, err := f.channels.CreateChannel(alice, &pb.CreateChannelRequest{ChannelId: "revoked", Title: "Закрытый", ViewType: "chat", MlsGroupId: bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	client := pb.NewMlsServiceClient(f.conn)
	_, err = client.PublishMls(alice, &pb.PublishMlsRequest{ChannelId: "revoked", RequestId: bytes.Repeat([]byte{1}, 16), Kind: "commit", Ciphertext: []byte("add"), AddGrantId: bob.GrantId, Welcome: []byte("welcome")})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	requireCode(t, requireMlsRetention(f.ctx, tx, bob.PrincipalId, "other-device", nil), codes.FailedPrecondition)
	_ = tx.Rollback(f.ctx)
	_, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET revoked_at=now() WHERE id=$1", bob.GrantId)
	if err != nil {
		t.Fatal(err)
	}
	message := &pb.PublishMlsRequest{ChannelId: "revoked", RequestId: bytes.Repeat([]byte{2}, 16), ExpectedEpoch: 1, Kind: "application", Ciphertext: []byte("message")}
	_, err = client.PublishMls(alice, message)
	requireCode(t, err, codes.FailedPrecondition)
	_, err = client.PublishMls(alice, &pb.PublishMlsRequest{ChannelId: "revoked", RequestId: bytes.Repeat([]byte{3}, 16), ExpectedEpoch: 1, Kind: "commit", Ciphertext: []byte("remove"), RemoveGrantId: bob.GrantId})
	if err != nil {
		t.Fatal(err)
	}
	message.ExpectedEpoch = 2
	if _, err = client.PublishMls(alice, message); err != nil {
		t.Fatal(err)
	}
	tx, err = f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if err = requireMlsRetention(f.ctx, tx, bob.PrincipalId, "other-device", nil); err != nil {
		t.Fatal(err)
	}
}

func TestMlsManagementTransferIsExclusiveAndIdempotent(t *testing.T) {
	f := newChannelFixture(t)
	alice, a := enrollMls(t, f, true)
	bob, b := enrollMls(t, f, false)
	_, outsider := enrollMls(t, f, false)
	_, err := f.channels.CreateChannel(alice, &pb.CreateChannelRequest{ChannelId: "management", Title: "Закрытый", ViewType: "chat", MlsGroupId: bytes.Repeat([]byte{8}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	rpc := pb.NewMlsServiceClient(f.conn)
	_, err = rpc.PublishMls(alice, &pb.PublishMlsRequest{ChannelId: "management", RequestId: bytes.Repeat([]byte{1}, 16), Kind: "commit", Ciphertext: []byte("add"), AddGrantId: b.GrantId, Welcome: []byte("welcome")})
	if err != nil {
		t.Fatal(err)
	}
	transfer := &pb.PublishMlsRequest{ChannelId: "management", RequestId: bytes.Repeat([]byte{2}, 16), ExpectedEpoch: 1, Kind: "transfer", Ciphertext: []byte("encrypted-manager"), NewManagerGrantId: a.GrantId}
	_, err = rpc.PublishMls(bob, transfer)
	requireCode(t, err, codes.PermissionDenied)
	transfer.NewManagerGrantId = outsider.GrantId
	_, err = rpc.PublishMls(alice, transfer)
	requireCode(t, err, codes.PermissionDenied)
	transfer.NewManagerGrantId = b.GrantId
	first, err := rpc.PublishMls(alice, transfer)
	if err != nil {
		t.Fatal(err)
	}
	again, err := rpc.PublishMls(alice, transfer)
	if err != nil || !proto.Equal(first, again) {
		t.Fatal("Повтор передачи изменил результат", err)
	}
	if first.Envelope.Epoch != 1 || first.Envelope.NewManagerGrantId != b.GrantId {
		t.Fatal(first)
	}
	remove := &pb.PublishMlsRequest{ChannelId: "management", RequestId: bytes.Repeat([]byte{3}, 16), ExpectedEpoch: 1, Kind: "commit", Ciphertext: []byte("remove"), RemoveGrantId: b.GrantId}
	_, err = rpc.PublishMls(alice, remove)
	requireCode(t, err, codes.PermissionDenied)
	_, err = rpc.PublishMls(bob, remove)
	requireCode(t, err, codes.PermissionDenied)
	remove.RemoveGrantId = a.GrantId
	if _, err = rpc.PublishMls(bob, remove); err != nil {
		t.Fatal("Новый управляющий не смог изменить состав", err)
	}
}
