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

func backupToken(n byte) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{n},32)) }
func backupID(n byte) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{n},16)) }
func backupCreate(t *testing.T, f *channelFixture, id string, witness bool) pb.RemoteBackupServiceClient {
 t.Helper(); api:=pb.NewRemoteBackupServiceClient(f.conn)
 read,write:=sha256.Sum256([]byte(backupToken(1))),sha256.Sum256([]byte(backupToken(2)))
 _,err:=api.CreateBackupSlot(f.owner,&pb.CreateBackupSlotRequest{Id:id,ReadHash:read[:],WriteHash:write[:],Witness:witness})
 if err!=nil { t.Fatal(err) }; return api
}
func backupRequest(id string, revision int64, parent string, data []byte) *pb.PutBackupRequest {
 return &pb.PutBackupRequest{Head:&pb.BackupHead{Id:id,Revision:revision,Parent:parent,Digest:backupHash(append([]byte("manifest"),data...)),ArchiveDigest:backupHash(data)},Token:backupToken(2),Archive:data}
}

func TestRemoteBackupCapabilitiesWitnessAndRestart(t *testing.T) {
 f,w:=newChannelFixture(t),newChannelFixture(t)
 id:=backupID(1); store,witness:=backupCreate(t,f,id,false),backupCreate(t,w,id,true)
 req:=backupRequest(id,1,"",[]byte("opaque encrypted bytes"))
 _,err:=store.CreateBackupSlot(f.ctx,&pb.CreateBackupSlotRequest{Id:backupID(2),ReadHash:bytes.Repeat([]byte{1},32),WriteHash:bytes.Repeat([]byte{2},32)})
 requireCode(t,err,codes.Unauthenticated)
 bad:=proto.Clone(req).(*pb.PutBackupRequest); bad.Token=backupToken(1)
 _,err=store.PutBackup(f.ctx,bad); requireCode(t,err,codes.NotFound)
 if _,err=store.PutBackup(f.ctx,req); err!=nil { t.Fatal(err) }
 if _,err=store.PutBackup(f.ctx,req); err!=nil { t.Fatal("Идемпотентный повтор",err) }
 changed:=proto.Clone(req).(*pb.PutBackupRequest); changed.Archive=[]byte("different"); changed.Head.ArchiveDigest=backupHash(changed.Archive)
 _,err=store.PutBackup(f.ctx,changed); requireCode(t,err,codes.Aborted)
 _,err=store.GetBackup(f.ctx,&pb.GetBackupRequest{Id:id,Token:backupToken(2),Revision:1}); requireCode(t,err,codes.NotFound)
 got,err:=store.GetBackup(f.ctx,&pb.GetBackupRequest{Id:id,Token:backupToken(1),Revision:1})
 if err!=nil || !bytes.Equal(got.GetArchive(),req.Archive) { t.Fatal(got,err) }
 _,err=witness.AdvanceBackupWitness(w.ctx,&pb.AdvanceBackupWitnessRequest{Head:req.Head,Token:backupToken(1)}); requireCode(t,err,codes.NotFound)
 if _,err=witness.AdvanceBackupWitness(w.ctx,&pb.AdvanceBackupWitnessRequest{Head:req.Head,Token:backupToken(2)}); err!=nil { t.Fatal(err) }
 r,err:=witness.ReadBackupWitness(w.ctx,&pb.ReadBackupWitnessRequest{Id:id,Token:backupToken(1),Challenge:backupToken(3)})
 if err!=nil || !ed25519.Verify(w.identity.PublicKey,backupWitnessText(r.GetHead(),backupToken(3)),r.GetSignature()) { t.Fatal(r,err) }
 if ed25519.Verify(w.identity.PublicKey,backupWitnessText(r.Head,backupToken(4)),r.Signature) { t.Fatal("Повтор подписи с другим challenge") }
 if ed25519.Verify(f.identity.PublicKey,backupWitnessText(r.Head,backupToken(3)),r.Signature) { t.Fatal("Подмена ключа свидетеля") }
 reopened,identity,err:=openManual(w.ctx,w.url); if err!=nil { t.Fatal(err) }; defer reopened.Close()
 after,err:=reopened.ReadBackupWitness(w.ctx,&pb.ReadBackupWitnessRequest{Id:id,Token:backupToken(1),Challenge:backupToken(4)})
 if err!=nil || !proto.Equal(after.GetHead(),r.Head) || !bytes.Equal(identity.PublicKey,w.identity.PublicKey) { t.Fatal(after,err) }
 var count int
 if err=w.store.pool.QueryRow(w.ctx,"SELECT count(*) FROM remote_backup_versions").Scan(&count); err!=nil || count!=0 { t.Fatal("Свидетель хранит контейнер",count,err) }
}

func TestRemoteBackupWitnessCASAndVersionLimits(t *testing.T) {
 f:=newChannelFixture(t); id:=backupID(2); api:=backupCreate(t,f,id,true)
 first:=backupRequest(id,1,"",[]byte{1}).Head
 _,err:=api.AdvanceBackupWitness(f.ctx,&pb.AdvanceBackupWitnessRequest{Head:first,Token:backupToken(2)}); if err!=nil { t.Fatal(err) }
 var wg sync.WaitGroup; results:=make(chan error,2)
 for _,n:=range []byte{2,3} { wg.Add(1); go func(n byte) { defer wg.Done(); h:=backupRequest(id,2,first.Digest,[]byte{n}).Head; _,err:=api.AdvanceBackupWitness(f.ctx,&pb.AdvanceBackupWitnessRequest{Head:h,Token:backupToken(2)}); results<-err }(n) }
 wg.Wait(); close(results); success,aborted:=0,0
 for err:=range results { if err==nil { success++ } else if codes.Aborted==status.Code(err) { aborted++ } else { t.Fatal(err) } }
 if success!=1 || aborted!=1 { t.Fatal(success,aborted) }
 _,err=api.AdvanceBackupWitness(f.ctx,&pb.AdvanceBackupWitnessRequest{Head:first,Token:backupToken(2)}); requireCode(t,err,codes.Aborted)
 _,err=api.PutBackup(f.ctx,backupRequest(id,1,"",[]byte{1})); requireCode(t,err,codes.NotFound)
 // Удалённая копия не затрагивает principals/grants и сохраняет прежние версии.
 s:=newChannelFixture(t); sid:=backupID(3); storage:=backupCreate(t,s,sid,false); parent:=""
 for revision:=int64(1); revision<=32; revision++ {
   req:=backupRequest(sid,revision,parent,[]byte{byte(revision)})
   if _,err=storage.PutBackup(s.ctx,req); err!=nil { t.Fatal(revision,err) }; parent=req.Head.Digest
 }
 _,err=storage.PutBackup(s.ctx,backupRequest(sid,33,parent,[]byte{33})); requireCode(t,err,codes.ResourceExhausted)
 _,err=storage.GetBackup(s.ctx,&pb.GetBackupRequest{Id:sid,Token:backupToken(1),Revision:1}); if err!=nil { t.Fatal(err) }
}

func TestRemoteBackupConnectLargeArchiveAndCreationRevocation(t *testing.T) {
 f:=newChannelFixture(t); id:=backupID(5); backupCreate(t,f,id,false)
 server:=httptest.NewServer(transport.WithConnect(http.NotFoundHandler(),f.conn,f.store)); defer server.Close()
 api:=rpc.NewRemoteBackupServiceClient(server.Client(),server.URL)
 req:=backupRequest(id,1,"",bytes.Repeat([]byte{7},5*1024*1024))
 _,err:=api.PutBackup(context.Background(),connect.NewRequest(req)); if err!=nil { t.Fatal(err) }
 got,err:=api.GetBackup(context.Background(),connect.NewRequest(&pb.GetBackupRequest{Id:id,Token:backupToken(1),Revision:1}))
 if err!=nil || !bytes.Equal(got.Msg.Archive,req.Archive) { t.Fatal("Полная загрузка/скачивание",err) }
 req.Archive=append(req.Archive,1); req.Head.ArchiveDigest=backupHash(req.Archive)
 _,err=api.PutBackup(context.Background(),connect.NewRequest(req)); if connect.CodeOf(err)!=connect.CodeInvalidArgument { t.Fatal(err) }
 caller,principal:=f.enroll(t,true)
 if _,err=f.store.pool.Exec(f.ctx,"UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1",principal); err!=nil { t.Fatal(err) }
 md,_:=metadata.FromOutgoingContext(caller); token:=md.Get("authorization")
 create:=connect.NewRequest(&pb.CreateBackupSlotRequest{Id:backupID(6),ReadHash:bytes.Repeat([]byte{1},32),WriteHash:bytes.Repeat([]byte{2},32)})
 create.Header().Set("Authorization",token[0]); _,err=api.CreateBackupSlot(context.Background(),create)
 if connect.CodeOf(err)!=connect.CodeUnauthenticated { t.Fatal(err) }
}

func TestRemoteBackupOwnerRevocationAndQuota(t *testing.T) {
 f:=newChannelFixture(t); id:=backupID(9); api:=backupCreate(t,f,id,false)
 other,_:=f.enroll(t,true)
 _,err:=api.RevokeBackupSlot(other,&pb.RevokeBackupSlotRequest{Id:id}); requireCode(t,err,codes.PermissionDenied)
 req:=backupRequest(id,1,"",[]byte{1}); if _,err=api.PutBackup(f.ctx,req); err!=nil { t.Fatal(err) }
 for range 2 { if _,err=api.RevokeBackupSlot(f.owner,&pb.RevokeBackupSlotRequest{Id:id}); err!=nil { t.Fatal(err) } }
 _,err=api.GetBackup(f.ctx,&pb.GetBackupRequest{Id:id,Token:backupToken(1),Revision:1}); requireCode(t,err,codes.NotFound)
 _,err=api.PutBackup(f.ctx,req); requireCode(t,err,codes.NotFound)
 read,write:=sha256.Sum256([]byte(backupToken(1))),sha256.Sum256([]byte(backupToken(2)))
 _,err=api.CreateBackupSlot(f.owner,&pb.CreateBackupSlotRequest{Id:id,ReadHash:read[:],WriteHash:write[:]}); requireCode(t,err,codes.AlreadyExists)
 for n:=byte(10);n<=12;n++ { backupCreate(t,f,backupID(n),false) }
 _,err=api.CreateBackupSlot(f.owner,&pb.CreateBackupSlotRequest{Id:backupID(13),ReadHash:read[:],WriteHash:write[:]}); requireCode(t,err,codes.ResourceExhausted)
 var count int
 if err=f.store.pool.QueryRow(f.ctx,"SELECT count(*) FROM remote_backup_versions WHERE slot_id=$1",id).Scan(&count); err!=nil || count!=1 { t.Fatal("Отзыв удалил контейнер",count,err) }
}
