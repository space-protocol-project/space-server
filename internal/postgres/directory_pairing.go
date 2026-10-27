package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const directoryNoiseProfile = "Noise_XX_25519_ChaChaPoly_SHA256"

func directoryCapability(value, prefix string) ([]byte, error) {
	if len(value) != 46 || !strings.HasPrefix(value, prefix) {
		return nil, status.Error(codes.InvalidArgument, "Неверная capability передачи")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value[3:])
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != value[3:] {
		return nil, status.Error(codes.InvalidArgument, "Неверная capability передачи")
	}
	digest := sha256.Sum256([]byte(value))
	return digest[:], nil
}

func (s *Store) directoryTransaction(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616043)"); err == nil {
		_, err = tx.Exec(ctx, "DELETE FROM directory_transfers WHERE expires_at<=now()")
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, databaseError(ctx, err)
	}
	return tx, nil
}

func (s *Store) CreateDirectoryTransfer(ctx context.Context, req *pb.CreateDirectoryTransferRequest) (*pb.CreateDirectoryTransferResponse, error) {
	if req.Profile != directoryNoiseProfile {
		return nil, status.Error(codes.InvalidArgument, "Неизвестный профиль передачи")
	}
	tx, err := s.directoryTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM directory_transfers").Scan(&count); err != nil {
		return nil, databaseError(ctx, err)
	}
	if count >= 32 {
		return nil, status.Error(codes.ResourceExhausted, "Лимит сессий передачи")
	}
	id, err := randomString("dt_")
	if err != nil {
		return nil, err
	}
	owner, err := randomString("dr_")
	if err != nil {
		return nil, err
	}
	code, err := randomString("dc_")
	if err != nil {
		return nil, err
	}
	ownerHash, _ := directoryCapability(owner, "dr_")
	codeHash, _ := directoryCapability(code, "dc_")
	now := time.Now().UTC().Truncate(time.Second)
	expiry := now.Add(5 * time.Minute)
	_, err = tx.Exec(ctx, "INSERT INTO directory_transfers(id,owner_hash,code_hash,created_at,expires_at) VALUES($1,$2,$3,$4,$5)", id, ownerHash, codeHash, now, expiry)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CreateDirectoryTransferResponse{Id: id, OwnerToken: owner, Code: code, CreatedAt: now.Unix(), ExpiresAt: expiry.Unix()}, nil
}

func (s *Store) OpenDirectoryTransfer(ctx context.Context, req *pb.OpenDirectoryTransferRequest) (*pb.OpenDirectoryTransferResponse, error) {
	code, err := directoryCapability(req.Code, "dc_")
	if err != nil {
		return nil, err
	}
	tx, err := s.directoryTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	peer, err := randomString("ds_")
	if err != nil {
		return nil, err
	}
	hash, _ := directoryCapability(peer, "ds_")
	result := &pb.OpenDirectoryTransferResponse{PeerToken: peer}
	var created, expiry time.Time
	err = tx.QueryRow(ctx, "UPDATE directory_transfers SET peer_hash=$2,code_hash=NULL WHERE code_hash=$1 AND expires_at>now() AND peer_hash IS NULL RETURNING id,created_at,expires_at", code, hash).Scan(&result.Id, &created, &expiry)
	if err != nil {
		return nil, pairError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	result.CreatedAt = created.Unix()
	result.ExpiresAt = expiry.Unix()
	return result, nil
}

type directoryLease struct {
	owner  bool
	next   int
	final  *int
	total  int
	expiry time.Time
}

func directorySession(ctx context.Context, tx pgx.Tx, id, token string) (directoryLease, error) {
	var lease directoryLease
	if len(id) != 46 || !strings.HasPrefix(id, "dt_") {
		return lease, status.Error(codes.InvalidArgument, "Неверная сессия")
	}
	prefix := "ds_"
	if strings.HasPrefix(token, "dr_") {
		prefix = "dr_"
	}
	hash, err := directoryCapability(token, prefix)
	if err != nil {
		return lease, err
	}
	err = tx.QueryRow(ctx, "SELECT owner_hash=$2,next_sequence,final_sequence,wire_bytes,expires_at FROM directory_transfers WHERE id=$1 AND (owner_hash=$2 OR peer_hash=$2) AND expires_at>now() FOR UPDATE", id, hash).Scan(&lease.owner, &lease.next, &lease.final, &lease.total, &lease.expiry)
	if err != nil {
		return lease, pairError(ctx, err)
	}
	return lease, nil
}

func directoryWriter(lease directoryLease, sequence int, final bool) bool {
	if lease.final != nil {
		return sequence == *lease.final+1 && lease.owner && !final
	}
	if sequence == 0 || sequence == 2 || sequence == 3 {
		return lease.owner && !final
	}
	if sequence == 1 {
		return !lease.owner && !final
	}
	return sequence >= 4 && !lease.owner
}

func (s *Store) WriteDirectoryFrame(ctx context.Context, req *pb.WriteDirectoryFrameRequest) (*pb.WriteDirectoryFrameResponse, error) {
	if req.Sequence >= 80 || len(req.Data) < 1 || len(req.Data) > 65535 {
		return nil, status.Error(codes.ResourceExhausted, "Предел record передачи")
	}
	tx, err := s.directoryTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	lease, err := directorySession(ctx, tx, req.Id, req.Token)
	if err != nil {
		return nil, err
	}
	if int(req.Sequence) < lease.next {
		return directoryRepeat(ctx, tx, req, lease)
	}
	if int(req.Sequence) != lease.next || !directoryWriter(lease, int(req.Sequence), req.FinalRecord) {
		return nil, status.Error(codes.FailedPrecondition, "Неверные роль или порядок передачи")
	}
	if lease.final != nil && (int(req.Sequence) != *lease.final+1 || len(req.Data) > 256) {
		return nil, status.Error(codes.FailedPrecondition, "Передача уже завершена")
	}
	var total int
	if err = tx.QueryRow(ctx, "SELECT COALESCE(sum(wire_bytes),0) FROM directory_transfers").Scan(&total); err != nil {
		return nil, databaseError(ctx, err)
	}
	if lease.total+len(req.Data) > 5*1024*1024 || total+len(req.Data) > 32*1024*1024 {
		return nil, status.Error(codes.ResourceExhausted, "Лимит хранения передачи")
	}
	_, err = tx.Exec(ctx, "INSERT INTO directory_transfer_frames(transfer_id,sequence,data,final) VALUES($1,$2,$3,$4)", req.Id, req.Sequence, req.Data, req.FinalRecord)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	_, err = tx.Exec(ctx, "UPDATE directory_transfers SET next_sequence=next_sequence+1,wire_bytes=wire_bytes+$2,final_sequence=CASE WHEN $3 THEN $4 ELSE final_sequence END WHERE id=$1", req.Id, len(req.Data), req.FinalRecord, req.Sequence)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.WriteDirectoryFrameResponse{}, nil
}

func directoryRepeat(ctx context.Context, tx pgx.Tx, req *pb.WriteDirectoryFrameRequest, lease directoryLease) (*pb.WriteDirectoryFrameResponse, error) {
	// Владелец старого slot выводится из роли, а не текущего next/final.
	owner := req.Sequence == 0 || req.Sequence == 2 || req.Sequence == 3
	if lease.final != nil && int(req.Sequence) == *lease.final+1 {
		owner = true
	}
	if owner != lease.owner {
		return nil, forbidden()
	}
	var data []byte
	var final bool
	err := tx.QueryRow(ctx, "SELECT data,final FROM directory_transfer_frames WHERE transfer_id=$1 AND sequence=$2", req.Id, req.Sequence).Scan(&data, &final)
	if err != nil {
		return nil, pairError(ctx, err)
	}
	if !bytes.Equal(data, req.Data) || final != req.FinalRecord {
		return nil, status.Error(codes.Aborted, "Record уже записан иначе")
	}
	return &pb.WriteDirectoryFrameResponse{}, nil
}

func (s *Store) ReadDirectoryFrame(ctx context.Context, req *pb.ReadDirectoryFrameRequest) (*pb.ReadDirectoryFrameResponse, error) {
	if req.Sequence >= 80 {
		return nil, status.Error(codes.InvalidArgument, "Неверный slot")
	}
	tx, err := s.directoryTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	lease, err := directorySession(ctx, tx, req.Id, req.Token)
	if err != nil {
		return nil, err
	}
	result := &pb.ReadDirectoryFrameResponse{ExpiresAt: lease.expiry.Unix()}
	err = tx.QueryRow(ctx, "SELECT data,final FROM directory_transfer_frames WHERE transfer_id=$1 AND sequence=$2", req.Id, req.Sequence).Scan(&result.Data, &result.FinalRecord)
	if err == nil {
		result.Available = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return result, nil
}

func (s *Store) CloseDirectoryTransfer(ctx context.Context, req *pb.CloseDirectoryTransferRequest) (*pb.CloseDirectoryTransferResponse, error) {
	tx, err := s.directoryTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = directorySession(ctx, tx, req.Id, req.Token); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM directory_transfers WHERE id=$1", req.Id); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CloseDirectoryTransferResponse{}, nil
}
