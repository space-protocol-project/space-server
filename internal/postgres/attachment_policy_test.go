package postgres

import (
	"context"
	"crypto/sha256"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"testing"
)

func setAttachmentPolicy(t *testing.T, f *channelFixture, actor context.Context, enabled bool) {
	t.Helper()
	current, err := f.admin.GetSettings(actor, &pb.GetSettingsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	s := current.Settings
	reply, err := f.admin.UpdateSettings(actor, &pb.UpdateSettingsRequest{Title: s.Title, ChatTitle: s.ChatTitle, ChatEnabled: s.ChatEnabled, RegistrationPolicy: s.RegistrationPolicy, ExpectedRevision: s.Revision, AttachmentsEnabled: &enabled})
	if err != nil || reply.Settings.GetAttachmentsEnabled() != enabled {
		t.Fatalf("Настройка вложений: %v %v", reply, err)
	}
}

func TestAttachmentPolicyEnforcementAndCatalog(t *testing.T) {
	f := newChannelFixture(t)
	client := pb.NewAssetServiceClient(f.conn)
	data := []byte("file")
	digest := sha256.Sum256(data)
	request := &pb.BeginUploadRequest{ChannelId: "general", Name: "file.txt", ByteSize: 4, Sha256: digest[:], IdempotencyKey: "policy"}
	uploaded, err := client.BeginUpload(f.owner, request)
	if err != nil {
		t.Fatal(err)
	}
	id := uploaded.Asset.Id
	if _, err = client.UploadChunk(f.owner, &pb.UploadChunkRequest{AssetId: id, Data: data}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.CompleteUpload(f.owner, &pb.CompleteUploadRequest{AssetId: id}); err != nil {
		t.Fatal(err)
	}
	reader, readerID := f.enroll(t, true)
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE memberships SET role='admin' WHERE principal_id=$1", readerID); err != nil {
		t.Fatal(err)
	}
	channels := pb.NewChannelServiceClient(f.conn)
	stream, err := channels.WatchChannels(reader, &pb.WatchChannelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || !first.AttachmentsEnabled {
		t.Fatal("Начальная политика", err)
	}
	setAttachmentPolicy(t, f, reader, false)
	changed, err := stream.Recv()
	if err != nil || changed.AttachmentsEnabled {
		t.Fatal("Обновление политики не доставлено", err)
	}
	_, err = client.BeginUpload(f.owner, request)
	requireCode(t, err, codes.PermissionDenied)
	_, err = client.UploadChunk(f.owner, &pb.UploadChunkRequest{AssetId: id, Data: data})
	requireCode(t, err, codes.PermissionDenied)
	_, err = client.CompleteUpload(f.owner, &pb.CompleteUploadRequest{AssetId: id})
	requireCode(t, err, codes.PermissionDenied)
	_, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", IdempotencyKey: "policy-msg", AttachmentIds: []string{id}})
	requireCode(t, err, codes.PermissionDenied)
	setAttachmentPolicy(t, f, reader, true)
	if _, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", IdempotencyKey: "policy-msg", AttachmentIds: []string{id}}); err != nil {
		t.Fatal(err)
	}
	setAttachmentPolicy(t, f, reader, false)
	_, err = client.DownloadChunk(f.owner, &pb.DownloadChunkRequest{AssetId: id})
	requireCode(t, err, codes.PermissionDenied)
	_, err = client.ListMyAssets(f.owner, &pb.ListMyAssetsRequest{ChannelId: "general"})
	requireCode(t, err, codes.PermissionDenied)
	_, err = client.ListChannelAssets(reader, &pb.ListChannelAssetsRequest{ChannelId: "general"})
	requireCode(t, err, codes.PermissionDenied)
	manifest, err := channels.GetManifest(reader, &pb.GetManifestRequest{})
	if err != nil || manifest.AttachmentsEnabled {
		t.Fatal("Политика manifest", err)
	}
	setAttachmentPolicy(t, f, reader, true)
	if _, err = client.DownloadChunk(reader, &pb.DownloadChunkRequest{AssetId: id}); err != nil {
		t.Fatal("Возврат доступа", err)
	}
}
