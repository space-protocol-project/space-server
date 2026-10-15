package postgres

import (
	"bytes"
	"connectrpc.com/connect"
	"crypto/sha256"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/status"
	"net/http"
	"net/http/httptest"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

func TestAssetUploadIntegrityAndOwnership(t *testing.T) {
	f := newChannelFixture(t)
	client := pb.NewAssetServiceClient(f.conn)
	data := bytes.Repeat([]byte("file"), assetChunkSize/4+2)
	hash := sha256.Sum256(data)
	request := &pb.BeginUploadRequest{ChannelId: "general", Name: "данные.txt", ByteSize: int64(len(data)), Sha256: hash[:], IdempotencyKey: "file-1"}
	started, err := client.BeginUpload(f.owner, request)
	if err != nil {
		t.Fatal(err)
	}
	id := started.Asset.Id
	if started.Asset.StorageGroup[:len(f.ownerID)] != f.ownerID {
		t.Fatal("Не сохранена группа автора")
	}
	part := &pb.UploadChunkRequest{AssetId: id, Data: data[:assetChunkSize]}
	if _, err = client.UploadChunk(f.owner, part); err != nil {
		t.Fatal(err)
	}
	if _, err = client.UploadChunk(f.owner, part); err != nil {
		t.Fatal("Повтор части:", err)
	}
	changed := append([]byte(nil), part.Data...)
	changed[0] ^= 1
	_, err = client.UploadChunk(f.owner, &pb.UploadChunkRequest{AssetId: id, Data: changed})
	requireCode(t, err, codes.AlreadyExists)
	_, err = client.CompleteUpload(f.owner, &pb.CompleteUploadRequest{AssetId: id})
	requireCode(t, err, codes.FailedPrecondition)
	if _, err = client.UploadChunk(f.owner, &pb.UploadChunkRequest{AssetId: id, Offset: assetChunkSize, Data: data[assetChunkSize:]}); err != nil {
		t.Fatal(err)
	}
	done, err := client.CompleteUpload(f.owner, &pb.CompleteUploadRequest{AssetId: id})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(done.Asset.Sha256, hash[:]) {
		t.Fatal("Изменилась контрольная сумма")
	}
	if _, err = client.CompleteUpload(f.owner, &pb.CompleteUploadRequest{AssetId: id}); err != nil {
		t.Fatal("Повтор завершения:", err)
	}
	replay, err := client.BeginUpload(f.owner, request)
	if err != nil || replay.Asset.Id != id || replay.ReceivedBytes != int64(len(data)) {
		t.Fatalf("Не восстановлена загрузка: %v %v", replay, err)
	}
	request.Name = "другой.txt"
	_, err = client.BeginUpload(f.owner, request)
	requireCode(t, err, codes.AlreadyExists)
	other, _ := f.enroll(t, true)
	_, err = client.CompleteUpload(other, &pb.CompleteUploadRequest{AssetId: id})
	requireCode(t, err, codes.PermissionDenied)
}

func TestAssetCompletionRejectsWrongHash(t *testing.T) {
	f := newChannelFixture(t)
	client := pb.NewAssetServiceClient(f.conn)
	expected := sha256.Sum256([]byte("expected"))
	started, err := client.BeginUpload(f.owner, &pb.BeginUploadRequest{ChannelId: "general", Name: "file.bin", ByteSize: 8, Sha256: expected[:], IdempotencyKey: "wrong-hash"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.UploadChunk(f.owner, &pb.UploadChunkRequest{AssetId: started.Asset.Id, Data: []byte("modified")}); err != nil {
		t.Fatal(err)
	}
	_, err = client.CompleteUpload(f.owner, &pb.CompleteUploadRequest{AssetId: started.Asset.Id})
	requireCode(t, err, codes.DataLoss)
	var ready bool
	if err = f.store.pool.QueryRow(f.ctx, "SELECT ready FROM assets WHERE id=$1", started.Asset.Id).Scan(&ready); err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("Повреждённый файл отмечен готовым")
	}
}

func TestPublishedAttachmentLifecycle(t *testing.T) {
	f := newChannelFixture(t)
	client := pb.NewAssetServiceClient(f.conn)
	data := []byte("attachment")
	digest := sha256.Sum256(data)
	uploaded, err := client.BeginUpload(f.owner, &pb.BeginUploadRequest{ChannelId: "general", Name: "file.txt", ByteSize: int64(len(data)), Sha256: digest[:], IdempotencyKey: "publish"})
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
	_, err = client.DownloadChunk(f.owner, &pb.DownloadChunkRequest{AssetId: id})
	requireCode(t, err, codes.NotFound)
	request := &pb.CreateContentRequest{ChannelId: "general", IdempotencyKey: "with-file", AttachmentIds: []string{id}}
	message, err := f.contents.CreateContent(f.owner, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Content.Attachments) != 1 {
		t.Fatal("Нет вложения в ответе")
	}
	replay, err := f.contents.CreateContent(f.owner, request)
	if err != nil || len(replay.Content.Attachments) != 1 {
		t.Fatalf("Повтор: %v %v", replay, err)
	}
	listed, err := f.contents.ListContent(f.owner, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || len(listed.Contents[0].Attachments) != 1 {
		t.Fatalf("История: %v %v", listed, err)
	}
	files, err := client.ListMyAssets(f.owner, &pb.ListMyAssetsRequest{ChannelId: "general"})
	if err != nil || len(files.Assets) != 1 {
		t.Fatalf("Мои файлы: %v %v", files, err)
	}
	downloaded, err := client.DownloadChunk(f.owner, &pb.DownloadChunkRequest{AssetId: id})
	if err != nil || !bytes.Equal(downloaded.Data, data) {
		t.Fatalf("Скачивание: %v", err)
	}
	_, err = client.CancelUpload(f.owner, &pb.CancelUploadRequest{AssetId: id})
	requireCode(t, err, codes.FailedPrecondition)
	updated, err := f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: message.Content.Id, ExpectedRevision: 1, Text: ""})
	if err != nil || len(updated.Content.Attachments) != 1 {
		t.Fatalf("Изменение: %v", err)
	}
	_, err = f.contents.DeleteContent(f.owner, &pb.DeleteContentRequest{ChannelId: "general", Id: message.Content.Id, ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DownloadChunk(f.owner, &pb.DownloadChunkRequest{AssetId: id})
	requireCode(t, err, codes.NotFound)
	files, err = client.ListMyAssets(f.owner, &pb.ListMyAssetsRequest{ChannelId: "general"})
	if err != nil || len(files.Assets) != 0 {
		t.Fatal("Удалённый файл остался в списке", err)
	}
}

func TestAssetStorageSettings(t *testing.T) {
	f := newChannelFixture(t)
	client := pb.NewAssetServiceClient(f.conn)
	current, err := f.admin.GetSettings(f.owner, &pb.GetSettingsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	total, personal := int64(100), int64(4)
	settings := current.Settings
	_, err = f.admin.UpdateSettings(f.owner, &pb.UpdateSettingsRequest{Title: settings.Title, ChatTitle: settings.ChatTitle, ChatEnabled: settings.ChatEnabled, RegistrationPolicy: settings.RegistrationPolicy, ExpectedRevision: settings.Revision, StorageLimitBytes: &total, MemberStorageLimitBytes: &personal})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("12345"))
	_, err = client.BeginUpload(f.owner, &pb.BeginUploadRequest{ChannelId: "general", Name: "file", ByteSize: 5, Sha256: digest[:], IdempotencyKey: "limit"})
	requireCode(t, err, codes.ResourceExhausted)
}

func TestAssetChunkThroughConnect(t *testing.T) {
	f := newChannelFixture(t)
	web := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer web.Close()
	client := rpc.NewAssetServiceClient(web.Client(), web.URL)
	data := bytes.Repeat([]byte("a"), assetChunkSize)
	digest := sha256.Sum256(data)
	begin := connect.NewRequest(&pb.BeginUploadRequest{ChannelId: "general", Name: "large.bin", ByteSize: int64(len(data)), Sha256: digest[:], IdempotencyKey: "connect"})
	begin.Header().Set("Authorization", outgoingAuthorization(f.owner))
	uploaded, err := client.BeginUpload(f.ctx, begin)
	if err != nil {
		t.Fatal(err)
	}
	chunk := connect.NewRequest(&pb.UploadChunkRequest{AssetId: uploaded.Msg.Asset.Id, Data: data})
	chunk.Header().Set("Authorization", outgoingAuthorization(f.owner))
	if _, err = client.UploadChunk(f.ctx, chunk); err != nil {
		t.Fatal(err)
	}
	done := connect.NewRequest(&pb.CompleteUploadRequest{AssetId: uploaded.Msg.Asset.Id})
	done.Header().Set("Authorization", outgoingAuthorization(f.owner))
	if _, err = client.CompleteUpload(f.ctx, done); err != nil {
		t.Fatal(err)
	}
}

func TestIndividualAssetStorageOverride(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, true)
	members, err := f.admin.ListMembers(f.owner, &pb.ListMembersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var revision int64
	for _, m := range members.Members {
		if m.PrincipalId == id {
			revision = m.Revision
		}
	}
	limit := int64(1)
	updated, err := f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: id, Role: "member", ExpectedRevision: revision, StorageLimitBytes: &limit})
	if err != nil || updated.Member.GetStorageLimitBytes() != 1 {
		t.Fatal("Не сохранён индивидуальный лимит", err)
	}
	digest := sha256.Sum256([]byte("12"))
	req := &pb.BeginUploadRequest{ChannelId: "general", Name: "file", ByteSize: 2, Sha256: digest[:], IdempotencyKey: "personal"}
	assets := pb.NewAssetServiceClient(f.conn)
	_, err = assets.BeginUpload(member, req)
	requireCode(t, err, codes.ResourceExhausted)
	_, err = f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: id, Role: "member", ExpectedRevision: updated.Member.Revision, ResetStorageLimit: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = assets.BeginUpload(member, req); err != nil {
		t.Fatal("Не восстановлен стандартный лимит", err)
	}
}

func TestPrivateAssetAccessIsCheckedForEveryDownload(t *testing.T) {
	f := newChannelFixture(t)
	channel := f.create(t, "private_files")
	f.acl(t, channel, roleRule("member", permissions(true, false, false, false)))
	member, _ := f.enroll(t, true)
	client := pb.NewAssetServiceClient(f.conn)
	data := []byte("private")
	digest := sha256.Sum256(data)
	begun, err := client.BeginUpload(f.owner, &pb.BeginUploadRequest{ChannelId: channel.Id, Name: "private.txt", ByteSize: int64(len(data)), Sha256: digest[:], IdempotencyKey: "private"})
	if err != nil {
		t.Fatal(err)
	}
	id := begun.Asset.Id
	if _, err = client.UploadChunk(f.owner, &pb.UploadChunkRequest{AssetId: id, Data: data}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.CompleteUpload(f.owner, &pb.CompleteUploadRequest{AssetId: id}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: channel.Id, IdempotencyKey: "private-message", AttachmentIds: []string{id}}); err != nil {
		t.Fatal(err)
	}
	_, err = client.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: id})
	requireCode(t, err, codes.PermissionDenied)
	f.acl(t, channel, roleRule("member", permissions(true, true, true, false)))
	if _, err = client.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: id}); err != nil {
		t.Fatal(err)
	}
	_, err = f.contents.CreateContent(member, &pb.CreateContentRequest{ChannelId: channel.Id, IdempotencyKey: "stolen", AttachmentIds: []string{id}})
	requireCode(t, err, codes.PermissionDenied)
	f.acl(t, channel, roleRule("member", permissions(true, false, false, false)))
	_, err = client.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: id})
	requireCode(t, err, codes.PermissionDenied)
}

func TestAssetQuotaSerializesReservationsAcrossChannels(t *testing.T) {
	f := newChannelFixture(t)
	second := f.create(t, "quota_second")
	if _, err := f.store.pool.Exec(f.ctx, "UPDATE space_settings SET storage_limit_bytes=10,member_storage_limit_bytes=100 WHERE singleton=true"); err != nil {
		t.Fatal(err)
	}
	client := pb.NewAssetServiceClient(f.conn)
	digest := sha256.Sum256([]byte("123456"))
	results := make(chan error, 2)
	for _, channel := range []string{"general", second.Id} {
		go func(channel string) {
			_, err := client.BeginUpload(f.owner, &pb.BeginUploadRequest{ChannelId: channel, Name: "file", ByteSize: 6, Sha256: digest[:], IdempotencyKey: channel})
			results <- err
		}(channel)
	}
	successes, denied := 0, 0
	for range 2 {
		err := <-results
		switch status.Code(err) {
		case codes.OK:
			successes++
		case codes.ResourceExhausted:
			denied++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || denied != 1 {
		t.Fatalf("Лимит превышен: успехов %d, отказов %d", successes, denied)
	}
}
