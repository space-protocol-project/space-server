package postgres

import (
 "bytes"
 "context"
 "crypto/ed25519"
 "crypto/sha256"
 "encoding/base64"
 "fmt"

 "github.com/jackc/pgx/v5"
 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 "github.com/space-protocol-project/space-server/internal/authn"
 "google.golang.org/grpc/codes"
 "google.golang.org/grpc/status"
)

func backupBytes(value string, length int) bool {
 raw, err := base64.RawURLEncoding.DecodeString(value)
 return err == nil && len(raw)==length && base64.RawURLEncoding.EncodeToString(raw)==value
}

func backupHeadValid(h *pb.BackupHead) bool {
 if h==nil || !backupBytes(h.Id,16) || h.Revision<1 || h.Revision>9007199254740991 { return false }
 return backupBytes(h.Digest,32) && backupBytes(h.ArchiveDigest,32) && ((h.Revision==1 && h.Parent=="") || (h.Revision>1 && backupBytes(h.Parent,32)))
}

func backupHash(raw []byte) string { h:=sha256.Sum256(raw); return base64.RawURLEncoding.EncodeToString(h[:]) }

func (s *Store) backupTransaction(ctx context.Context) (pgx.Tx,error) {
 tx,err:=s.pool.Begin(ctx)
 if err!=nil { return nil,databaseError(ctx,err) }
 if _,err=tx.Exec(ctx,"SELECT pg_advisory_xact_lock(73616044)"); err!=nil { tx.Rollback(ctx); return nil,databaseError(ctx,err) }
 return tx,nil
}

func (s *Store) createBackupSlot(ctx context.Context, req *pb.CreateBackupSlotRequest) (*pb.BackupHead,error) {
 if !backupBytes(req.Id,16) || len(req.ReadHash)!=32 || len(req.WriteHash)!=32 || bytes.Equal(req.ReadHash,req.WriteHash) { return nil,status.Error(codes.InvalidArgument,"Неверный слот копии") }
 if authn.Token(ctx)=="" { return nil,denied() }
 tx,err:=s.backupTransaction(ctx); if err!=nil { return nil,err }; defer tx.Rollback(ctx)
 if err=s.lockSession(ctx,tx); err!=nil { return nil,err }
 member,err:=membership(ctx,tx,authn.Actor(ctx),false)
 if err!=nil { return nil,err }; if member.Blocked { return nil,forbidden() }
 var count,own int
 if err=tx.QueryRow(ctx,"SELECT count(*),count(*) FILTER(WHERE owner_id=$1) FROM remote_backup_slots",authn.Actor(ctx)).Scan(&count,&own); err!=nil { return nil,databaseError(ctx,err) }
 // Повтор создания не меняет права и не расходует дополнительную квоту.
 var same bool
 if err=tx.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM remote_backup_slots WHERE id=$1 AND owner_id=$2 AND read_hash=$3 AND write_hash=$4 AND witness=$5 AND NOT revoked)",req.Id,authn.Actor(ctx),req.ReadHash,req.WriteHash,req.Witness).Scan(&same); err!=nil { return nil,databaseError(ctx,err) }
 if same { return &pb.BackupHead{Id:req.Id},nil }
 if count>=64 || own>=4 { return nil,status.Error(codes.ResourceExhausted,"Квота слотов копий") }
 tag,err:=tx.Exec(ctx,"INSERT INTO remote_backup_slots(id,owner_id,read_hash,write_hash,witness) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING",req.Id,authn.Actor(ctx),req.ReadHash,req.WriteHash,req.Witness)
 if err!=nil { return nil,databaseError(ctx,err) }; if tag.RowsAffected()!=1 { return nil,status.Error(codes.AlreadyExists,"Слот занят") }
 if err=tx.Commit(ctx); err!=nil { return nil,databaseError(ctx,err) }
 return &pb.BackupHead{Id:req.Id},nil
}

func backupSlot(ctx context.Context, tx pgx.Tx, id,token string, write,witness bool) (*pb.BackupHead,error) {
 if !backupBytes(id,16) || !backupBytes(token,32) { return nil,denied() }
 h:=sha256.Sum256([]byte(token)); column:="read_hash"; if write { column="write_hash" }
 result:=&pb.BackupHead{Id:id}
 err:=tx.QueryRow(ctx,"SELECT revision,digest,parent,archive_digest FROM remote_backup_slots WHERE id=$1 AND "+column+"=$2 AND witness=$3 AND NOT revoked FOR UPDATE",id,h[:],witness).Scan(&result.Revision,&result.Digest,&result.Parent,&result.ArchiveDigest)
 if err!=nil { return nil,pairError(ctx,err) }; return result,nil
}

func sameBackup(a,b *pb.BackupHead) bool { return a.Id==b.Id && a.Revision==b.Revision && a.Digest==b.Digest && a.Parent==b.Parent && a.ArchiveDigest==b.ArchiveDigest }

func nextBackup(old,next *pb.BackupHead) error {
 if next.Revision!=old.Revision+1 || next.Parent!=old.Digest { return status.Error(codes.Aborted,"Голова копии изменилась или образует ветвление") }; return nil
}

func saveBackupHead(ctx context.Context, tx pgx.Tx, h *pb.BackupHead) error {
 _,err:=tx.Exec(ctx,"UPDATE remote_backup_slots SET revision=$2,digest=$3,parent=$4,archive_digest=$5 WHERE id=$1",h.Id,h.Revision,h.Digest,h.Parent,h.ArchiveDigest)
 return err
}

func (s *Store) putBackup(ctx context.Context, req *pb.PutBackupRequest) (*pb.BackupHead,error) {
 if !backupHeadValid(req.Head) || len(req.Archive)==0 || len(req.Archive)>5*1024*1024 || backupHash(req.Archive)!=req.Head.ArchiveDigest { return nil,status.Error(codes.InvalidArgument,"Неверная копия") }
 tx,err:=s.backupTransaction(ctx); if err!=nil { return nil,err }; defer tx.Rollback(ctx)
 old,err:=backupSlot(ctx,tx,req.Head.Id,req.Token,true,false); if err!=nil { return nil,err }
 if sameBackup(old,req.Head) { return req.Head,nil }
 if err=nextBackup(old,req.Head); err!=nil { return nil,err }
 var count,total int64
 if err=tx.QueryRow(ctx,"SELECT count(*) FILTER(WHERE slot_id=$1),COALESCE(sum(octet_length(archive)),0) FROM remote_backup_versions",req.Head.Id).Scan(&count,&total); err!=nil { return nil,databaseError(ctx,err) }
 if count>=32 || total+int64(len(req.Archive))>256*1024*1024 { return nil,status.Error(codes.ResourceExhausted,"Квота хранения копий") }
 _,err=tx.Exec(ctx,"INSERT INTO remote_backup_versions(slot_id,revision,digest,parent,archive_digest,archive) VALUES($1,$2,$3,$4,$5,$6)",req.Head.Id,req.Head.Revision,req.Head.Digest,req.Head.Parent,req.Head.ArchiveDigest,req.Archive)
 if err==nil { err=saveBackupHead(ctx,tx,req.Head) }; if err!=nil { return nil,databaseError(ctx,err) }
 if err=tx.Commit(ctx); err!=nil { return nil,databaseError(ctx,err) }; return req.Head,nil
}

func (s *Store) GetBackup(ctx context.Context, req *pb.GetBackupRequest) (*pb.GetBackupResponse,error) {
 tx,err:=s.backupTransaction(ctx); if err!=nil { return nil,err }; defer tx.Rollback(ctx)
 if _,err=backupSlot(ctx,tx,req.Id,req.Token,false,false); err!=nil { return nil,err }
 result:=&pb.GetBackupResponse{Head:&pb.BackupHead{Id:req.Id,Revision:req.Revision}}
 err=tx.QueryRow(ctx,"SELECT digest,parent,archive_digest,archive FROM remote_backup_versions WHERE slot_id=$1 AND revision=$2",req.Id,req.Revision).Scan(&result.Head.Digest,&result.Head.Parent,&result.Head.ArchiveDigest,&result.Archive)
 if err!=nil { return nil,pairError(ctx,err) }; return result,nil
}

func (s *Store) advanceBackupWitness(ctx context.Context, req *pb.AdvanceBackupWitnessRequest) (*pb.BackupHead,error) {
 if !backupHeadValid(req.Head) { return nil,status.Error(codes.InvalidArgument,"Неверная голова") }
 tx,err:=s.backupTransaction(ctx); if err!=nil { return nil,err }; defer tx.Rollback(ctx)
 old,err:=backupSlot(ctx,tx,req.Head.Id,req.Token,true,true); if err!=nil { return nil,err }
 if sameBackup(old,req.Head) { return req.Head,nil }
 if err=nextBackup(old,req.Head); err!=nil { return nil,err }
 if err=saveBackupHead(ctx,tx,req.Head); err!=nil { return nil,databaseError(ctx,err) }
 if err=tx.Commit(ctx); err!=nil { return nil,databaseError(ctx,err) }; return req.Head,nil
}

func backupWitnessText(h *pb.BackupHead, challenge string) []byte {
 return []byte(fmt.Sprintf("space/backup-witness/v1\n%s\n%s\n%d\n%s\n%s\n%s",challenge,h.Id,h.Revision,h.Digest,h.Parent,h.ArchiveDigest))
}

func (s *Store) CreateBackupSlot(ctx context.Context, req *pb.CreateBackupSlotRequest) (*pb.CreateBackupSlotResponse,error) {
 h,err:=s.createBackupSlot(ctx,req); if err!=nil { return nil,err }; return &pb.CreateBackupSlotResponse{Head:h},nil
}
func (s *Store) RevokeBackupSlot(ctx context.Context, req *pb.RevokeBackupSlotRequest) (*pb.RevokeBackupSlotResponse,error) {
 if !backupBytes(req.Id,16) { return nil,status.Error(codes.InvalidArgument,"Неверный слот") }
 if authn.Token(ctx)=="" { return nil,denied() }
 tx,err:=s.backupTransaction(ctx); if err!=nil { return nil,err }; defer tx.Rollback(ctx)
 if err=s.lockSession(ctx,tx); err!=nil { return nil,err }
 tag,err:=tx.Exec(ctx,"UPDATE remote_backup_slots SET revoked=true WHERE id=$1 AND owner_id=$2",req.Id,authn.Actor(ctx))
 if err!=nil { return nil,databaseError(ctx,err) }; if tag.RowsAffected()!=1 { return nil,forbidden() }
 if err=tx.Commit(ctx); err!=nil { return nil,databaseError(ctx,err) }; return &pb.RevokeBackupSlotResponse{},nil
}
func (s *Store) PutBackup(ctx context.Context, req *pb.PutBackupRequest) (*pb.PutBackupResponse,error) {
 h,err:=s.putBackup(ctx,req); if err!=nil { return nil,err }; return &pb.PutBackupResponse{Head:h},nil
}
func (s *Store) AdvanceBackupWitness(ctx context.Context, req *pb.AdvanceBackupWitnessRequest) (*pb.AdvanceBackupWitnessResponse,error) {
 h,err:=s.advanceBackupWitness(ctx,req); if err!=nil { return nil,err }; return &pb.AdvanceBackupWitnessResponse{Head:h},nil
}

func (s *Store) ReadBackupWitness(ctx context.Context, req *pb.ReadBackupWitnessRequest) (*pb.ReadBackupWitnessResponse,error) {
 if !backupBytes(req.Challenge,32) { return nil,status.Error(codes.InvalidArgument,"Неверный challenge") }
 tx,err:=s.backupTransaction(ctx); if err!=nil { return nil,err }; defer tx.Rollback(ctx)
 h,err:=backupSlot(ctx,tx,req.Id,req.Token,false,true); if err!=nil { return nil,err }
 var seed []byte
 if err=tx.QueryRow(ctx,"SELECT signing_seed FROM server_state WHERE singleton=true").Scan(&seed); err!=nil { return nil,databaseError(ctx,err) }
 defer clear(seed)
 key:=ed25519.NewKeyFromSeed(seed); defer clear(key)
 return &pb.ReadBackupWitnessResponse{Head:h,Challenge:req.Challenge,Signature:ed25519.Sign(key,backupWitnessText(h,req.Challenge))},nil
}
