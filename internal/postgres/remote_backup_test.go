package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func backupToken(n byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{n}, 32))
}
func backupID(n byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{n}, 16))
}
func backupCreate(t *testing.T, f *channelFixture, id string) pb.RemoteBackupServiceClient {
	t.Helper()
	api := pb.NewRemoteBackupServiceClient(f.conn)
	read, write := sha256.Sum256([]byte(backupToken(1))), sha256.Sum256([]byte(backupToken(2)))
	_, err := api.CreateBackupSlot(f.owner, &pb.CreateBackupSlotRequest{Id: id, ReadHash: read[:], WriteHash: write[:]})
	if err != nil {
		t.Fatal(err)
	}
	return api
}
func backupRequest(id string, revision int64, parent string, data []byte) *pb.PutBackupRequest {
	head := &pb.BackupHead{Id: id, Revision: revision, Parent: parent, ArchiveDigest: backupHash(data)}
	head.Digest = backupChainDigest(head)
	return &pb.PutBackupRequest{Head: head, Token: backupToken(2), Archive: data}
}

func TestRemoteBackupAtomicPublicationCapabilitiesAndRestart(t *testing.T) {
 f:=newChannelFixture(t); id:=backupID(1); api:=backupCreate(t,f,id)
 req:=backupRequest(id,1,"",[]byte("opaque encrypted archive"))
 wrong:=proto.Clone(req).(*pb.PutBackupRequest); wrong.Head.Digest=backupToken(19)
 _,err:=api.PutBackup(f.ctx,wrong); requireCode(t,err,codes.InvalidArgument)
 wrong=proto.Clone(req).(*pb.PutBackupRequest); wrong.Token=backupToken(1)
 _,err=api.PutBackup(f.ctx,wrong); requireCode(t,err,codes.NotFound)
 _,err=api.CreateBackupSlot(f.ctx,&pb.CreateBackupSlotRequest{Id:backupID(2),ReadHash:bytes.Repeat([]byte{1},32),WriteHash:bytes.Repeat([]byte{2},32)})
 requireCode(t,err,codes.Unauthenticated)
 if _,err=api.PutBackup(f.ctx,req); err!=nil {t.Fatal(err)}
 if _,err=api.PutBackup(f.ctx,req); err!=nil {t.Fatal(err)}
 head,err:=api.ReadBackupHead(f.ctx,&pb.ReadBackupHeadRequest{Id:id,Token:backupToken(1),Challenge:backupToken(3)})
 if err!=nil || !proto.Equal(head.GetHead(),req.Head) {t.Fatal(head,err)}
 if !ed25519.Verify(f.identity.PublicKey,backupHeadProof(head.Head,backupToken(3)),head.Signature) {t.Fatal("Неверная подпись пространства")}
 if ed25519.Verify(f.identity.PublicKey,backupHeadProof(head.Head,backupToken(4)),head.Signature) {t.Fatal("Повтор challenge")}
 _,err=api.GetBackup(f.ctx,&pb.GetBackupRequest{Id:id,Token:backupToken(2),Revision:1}); requireCode(t,err,codes.NotFound)
 reopened,identity,err:=openManual(f.ctx,f.url); if err!=nil {t.Fatal(err)}; defer reopened.Close()
 got,err:=reopened.GetBackup(f.ctx,&pb.GetBackupRequest{Id:id,Token:backupToken(1),Revision:1})
 if err!=nil || !bytes.Equal(got.GetArchive(),req.Archive) || !bytes.Equal(identity.PublicKey,f.identity.PublicKey) {t.Fatal(got,err)}
 after,err:=reopened.ReadBackupHead(f.ctx,&pb.ReadBackupHeadRequest{Id:id,Token:backupToken(1),Challenge:backupToken(4)})
 if err!=nil || !proto.Equal(after.GetHead(),got.GetHead()) {t.Fatal(after,err)}
}
func TestRemoteBackupConcurrentCASAndVersionLimits(t *testing.T) {
 f:=newChannelFixture(t); id:=backupID(2); api:=backupCreate(t,f,id)
 var wg sync.WaitGroup; results:=make(chan error,2)
 for _,n:=range []byte{2,3} {wg.Add(1); go func(n byte) {defer wg.Done(); _,err:=api.PutBackup(f.ctx,backupRequest(id,1,"",[]byte{n})); results<-err}(n)}
 wg.Wait(); close(results); success,aborted:=0,0
 for err:=range results {if err==nil {success++} else if status.Code(err)==codes.Aborted {aborted++} else {t.Fatal(err)}}
 if success!=1 || aborted!=1 {t.Fatal(success,aborted)}
 h,err:=api.ReadBackupHead(f.ctx,&pb.ReadBackupHeadRequest{Id:id,Token:backupToken(1),Challenge:backupToken(3)})
 if err!=nil {t.Fatal(err)}
 got,err:=api.GetBackup(f.ctx,&pb.GetBackupRequest{Id:id,Token:backupToken(1),Revision:1})
 if err!=nil || !proto.Equal(got.GetHead(),h.Head) {t.Fatal(got,err)}
 parent:=h.Head.Digest
 for revision:=int64(2);revision<=32;revision++ {req:=backupRequest(id,revision,parent,[]byte{byte(revision)}); if _,err=api.PutBackup(f.ctx,req);err!=nil {t.Fatal(err)};parent=req.Head.Digest}
 _,err=api.PutBackup(f.ctx,backupRequest(id,33,parent,[]byte{33})); requireCode(t,err,codes.ResourceExhausted)
 _,err=api.GetBackup(f.ctx,&pb.GetBackupRequest{Id:id,Token:backupToken(1),Revision:1});if err!=nil {t.Fatal(err)}
}
func TestRemoteBackupConnectLargeArchiveAndCreationRevocation(t *testing.T) {
	f := newChannelFixture(t)
	id := backupID(5)
	backupCreate(t, f, id)
	server := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer server.Close()
	api := rpc.NewRemoteBackupServiceClient(server.Client(), server.URL)
	req := backupRequest(id, 1, "", bytes.Repeat([]byte{7}, 5*1024*1024))
	_, err := api.PutBackup(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	got, err := api.GetBackup(context.Background(), connect.NewRequest(&pb.GetBackupRequest{Id: id, Token: backupToken(1), Revision: 1}))
	if err != nil || !bytes.Equal(got.Msg.Archive, req.Archive) {
		t.Fatal("Полная загрузка/скачивание", err)
	}
	req.Archive = append(req.Archive, 1)
	req.Head.ArchiveDigest = backupHash(req.Archive)
	_, err = api.PutBackup(context.Background(), connect.NewRequest(req))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatal(err)
	}
	caller, principal := f.enroll(t, true)
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", principal); err != nil {
		t.Fatal(err)
	}
	md, _ := metadata.FromOutgoingContext(caller)
	token := md.Get("authorization")
	create := connect.NewRequest(&pb.CreateBackupSlotRequest{Id: backupID(6), ReadHash: bytes.Repeat([]byte{1}, 32), WriteHash: bytes.Repeat([]byte{2}, 32)})
	create.Header().Set("Authorization", token[0])
	_, err = api.CreateBackupSlot(context.Background(), create)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal(err)
	}
}

func TestRemoteBackupOwnerRevocationAndQuota(t *testing.T) {
	f := newChannelFixture(t)
	id := backupID(9)
	api := backupCreate(t, f, id)
	other, _ := f.enroll(t, true)
	_, err := api.RevokeBackupSlot(other, &pb.RevokeBackupSlotRequest{Id: id})
	requireCode(t, err, codes.PermissionDenied)
	req := backupRequest(id, 1, "", []byte{1})
	if _, err = api.PutBackup(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = api.RevokeBackupSlot(f.owner, &pb.RevokeBackupSlotRequest{Id: id}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = api.GetBackup(f.ctx, &pb.GetBackupRequest{Id: id, Token: backupToken(1), Revision: 1})
	requireCode(t, err, codes.NotFound)
	_, err = api.PutBackup(f.ctx, req)
	requireCode(t, err, codes.NotFound)
	read, write := sha256.Sum256([]byte(backupToken(1))), sha256.Sum256([]byte(backupToken(2)))
	_, err = api.CreateBackupSlot(f.owner, &pb.CreateBackupSlotRequest{Id: id, ReadHash: read[:], WriteHash: write[:]})
	requireCode(t, err, codes.AlreadyExists)
	for n := byte(10); n <= 12; n++ {
		backupCreate(t, f, backupID(n))
	}
	_, err = api.CreateBackupSlot(f.owner, &pb.CreateBackupSlotRequest{Id: backupID(13), ReadHash: read[:], WriteHash: write[:]})
	requireCode(t, err, codes.ResourceExhausted)
	var count int
	if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM remote_backup_versions WHERE slot_id=$1", id).Scan(&count); err != nil || count != 1 {
		t.Fatal("Отзыв удалил контейнер", count, err)
	}
}

func TestRemoteBackupMigrationPreservesSpaceArchives(t *testing.T) {
 f:=newChannelFixture(t); id:=backupID(20); api:=backupCreate(t,f,id)
 req:=backupRequest(id,1,"",[]byte("preserved archive"))
 if _,err:=api.PutBackup(f.ctx,req);err!=nil {t.Fatal(err)}
 if _,err:=f.store.pool.Exec(f.ctx,"ALTER TABLE remote_backup_slots ADD COLUMN witness boolean NOT NULL DEFAULT false; DELETE FROM schema_migrations WHERE version=45");err!=nil {t.Fatal(err)}
 _,err:=f.store.pool.Exec(f.ctx,"INSERT INTO remote_backup_slots(id,owner_id,read_hash,write_hash,witness) VALUES($1,$2,$3,$4,true)",backupID(21),f.ownerID,bytes.Repeat([]byte{3},32),bytes.Repeat([]byte{4},32))
 if err!=nil {t.Fatal(err)}
 reopened,identity,err:=openManual(f.ctx,f.url);if err!=nil {t.Fatal(err)};defer reopened.Close()
 got,err:=reopened.GetBackup(f.ctx,&pb.GetBackupRequest{Id:id,Token:backupToken(1),Revision:1})
 if err!=nil || !bytes.Equal(got.GetArchive(),req.Archive) || !bytes.Equal(identity.PublicKey,f.identity.PublicKey) {t.Fatal(got,err)}
 var count int
 if err=reopened.pool.QueryRow(f.ctx,"SELECT count(*) FROM remote_backup_slots").Scan(&count);err!=nil || count!=1 {t.Fatal(count,err)}
 if err=reopened.pool.QueryRow(f.ctx,"SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='remote_backup_slots' AND column_name='witness'").Scan(&count);err!=nil || count!=0 {t.Fatal(count,err)}
}
