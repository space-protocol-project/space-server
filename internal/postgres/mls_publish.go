package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"time"
)

func validateMlsPublish(req *pb.PublishMlsRequest) error {
	if len(req.RequestId) != 16 || req.ExpectedEpoch < 0 || len(req.Ciphertext) == 0 || len(req.Ciphertext) > 262144 || len(req.Welcome) > 262144 || len(req.AttachmentIds) > 5 {
		return status.Error(codes.InvalidArgument, "Некорректный MLS-пакет")
	}
	if (req.Kind == "application" || req.Kind == "transfer") && req.AddGrantId == "" && req.RemoveGrantId == "" && len(req.Welcome) == 0 {
		return validateMlsTransfer(req)
	}
	if req.Kind == "update" && req.AddGrantId == "" && req.RemoveGrantId == "" && req.NewManagerGrantId == "" && len(req.Welcome) == 0 && len(req.AttachmentIds) == 0 {
		return nil
	}
	if req.Kind == "commit" && req.NewManagerGrantId == "" && len(req.AttachmentIds) == 0 && (req.AddGrantId != "") != (req.RemoveGrantId != "") && (req.AddGrantId != "") == (len(req.Welcome) > 0) {
		return nil
	}
	return status.Error(codes.InvalidArgument, "Некорректная операция MLS")
}

func (s *Store) PublishMls(ctx context.Context, req *pb.PublishMlsRequest) (*pb.PublishMlsResponse, error) {
	if err := validateMlsPublish(req); err != nil {
		return nil, err
	}
	tx, err := s.mlsTransaction(ctx, req.ChannelId, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	scope, err := sessionMlsScope(ctx, tx)
	if err != nil {
		return nil, err
	}
	if _, err = activeMlsMember(ctx, tx, req.ChannelId, scope.GrantID); err != nil {
		return nil, err
	}
	var epoch, sequence int64
	if err = tx.QueryRow(ctx, "SELECT epoch,sequence FROM mls_channels WHERE channel_id=$1 FOR UPDATE", req.ChannelId).Scan(&epoch, &sequence); err != nil {
		return nil, databaseError(ctx, err)
	}
	raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	hash := sha256.Sum256(raw)
	previous, err := replayMls(ctx, tx, req, hash[:], scope.GrantID)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		return &pb.PublishMlsResponse{Envelope: previous}, nil
	}
	if req.Kind != "update" {
		if _, err = channelAccess(ctx, tx, req.ChannelId, "write", false); err != nil {
			return nil, err
		}
	}
	if err = requireMlsManager(ctx, tx, req, scope.GrantID); err != nil {
		return nil, err
	}
	if epoch != req.ExpectedEpoch {
		return nil, status.Error(codes.Aborted, "Эпоха MLS изменилась; сначала получите новые события")
	}
	envelope, err := insertMlsEnvelope(ctx, tx, req, scope, epoch, sequence, hash[:])
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.PublishMlsResponse{Envelope: envelope}, nil
}

func replayMls(ctx context.Context, tx pgx.Tx, req *pb.PublishMlsRequest, hash []byte, grant string) (*pb.MlsEnvelope, error) {
	var savedHash, encoded []byte
	err := tx.QueryRow(ctx, "SELECT request_hash,envelope FROM mls_envelopes WHERE channel_id=$1 AND request_id=$2", req.ChannelId, req.RequestId).Scan(&savedHash, &encoded)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	result := new(pb.MlsEnvelope)
	if proto.Unmarshal(encoded, result) != nil {
		return nil, status.Error(codes.Internal, "Повреждён MLS-пакет")
	}
	if result.AuthorGrantId != grant || !bytes.Equal(hash, savedHash) {
		return nil, status.Error(codes.AlreadyExists, "Ключ MLS-запроса уже использован")
	}
	return result, nil
}

func updateMlsMembers(ctx context.Context, tx pgx.Tx, req *pb.PublishMlsRequest, sequence int64) error {
	if req.Kind != "commit" {
		return nil
	}
	if req.RemoveGrantId != "" {
		result, err := tx.Exec(ctx, "UPDATE mls_members SET left_sequence=$3 WHERE channel_id=$1 AND grant_id=$2 AND left_sequence IS NULL", req.ChannelId, req.RemoveGrantId, sequence)
		if err != nil {
			return databaseError(ctx, err)
		}
		if result.RowsAffected() != 1 {
			return status.Error(codes.NotFound, "Устройство не входит в группу")
		}
		return nil
	}
	var principal string
	err := tx.QueryRow(ctx, `SELECT g.principal_id FROM mls_devices d JOIN mls_device_routes route ON route.leaf_grant_id=d.grant_id JOIN device_grants g ON g.id=route.current_grant_id JOIN principals p ON p.id=g.principal_id JOIN memberships m ON m.principal_id=p.id WHERE d.grant_id=$1 AND NOT d.consumed AND g.revoked_at IS NULL AND g.expires_at>now() AND g.auth_epoch=p.auth_epoch AND NOT m.blocked AND 'chat.read'=ANY(g.scopes)`+activeParentCondition+activePairCondition+` FOR UPDATE OF d`, req.AddGrantId).Scan(&principal)
	if err == pgx.ErrNoRows {
		return status.Error(codes.FailedPrecondition, "Нет действующего KeyPackage")
	}
	if err != nil {
		return databaseError(ctx, err)
	}
	member, err := membership(ctx, tx, principal, false)
	if err != nil {
		return err
	}
	permissions, err := effectivePermissions(ctx, tx, req.ChannelId, member)
	if err != nil {
		return err
	}
	if !permissions.Read {
		return forbidden()
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mls_members WHERE channel_id=$1 AND left_sequence IS NULL", req.ChannelId).Scan(&count); err != nil {
		return databaseError(ctx, err)
	}
	if count >= 256 {
		return status.Error(codes.ResourceExhausted, "В группе уже 256 устройств")
	}
	added, err := tx.Exec(ctx, `INSERT INTO mls_members(channel_id,grant_id,joined_sequence) VALUES($1,$2,$3) ON CONFLICT(channel_id,grant_id) DO UPDATE SET joined_sequence=EXCLUDED.joined_sequence,left_sequence=NULL WHERE mls_members.left_sequence IS NOT NULL`, req.ChannelId, req.AddGrantId, sequence)
	if err != nil {
		return databaseError(ctx, err)
	}
	if added.RowsAffected() != 1 {
		return status.Error(codes.AlreadyExists, "Устройство уже входит в группу")
	}
	_, err = tx.Exec(ctx, "UPDATE mls_devices SET consumed=true WHERE grant_id=$1", req.AddGrantId)
	return databaseErrorOrNil(ctx, err)
}

func bindMlsAssets(ctx context.Context, tx pgx.Tx, req *pb.PublishMlsRequest, author string, sequence int64) error {
	if len(req.AttachmentIds) == 0 {
		return nil
	}
	if err := requireAttachmentsEnabled(ctx, tx); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range req.AttachmentIds {
		if seen[id] {
			return status.Error(codes.InvalidArgument, "Повтор вложения")
		}
		seen[id] = true
		result, err := tx.Exec(ctx, `UPDATE assets SET mls_sequence=$4 WHERE id=$1 AND channel_id=$2 AND author_id=$3 AND ready AND content_id IS NULL AND mls_sequence IS NULL AND name='encrypted-file'`, id, req.ChannelId, author, sequence)
		if err != nil {
			return databaseError(ctx, err)
		}
		if result.RowsAffected() != 1 {
			return status.Error(codes.FailedPrecondition, "Вложение не готово или принадлежит другому сообщению")
		}
	}
	return nil
}

func insertMlsEnvelope(ctx context.Context, tx pgx.Tx, req *pb.PublishMlsRequest, scope mlsScope, epoch, sequence int64, hash []byte) (*pb.MlsEnvelope, error) {
	var err error
	if req.Kind == "application" || req.Kind == "update" {
		if err := requireHealthyMlsRoster(ctx, tx, req.ChannelId); err != nil {
			return nil, err
		}
	}
	sequence++
	if err = updateMlsMembers(ctx, tx, req, sequence); err != nil {
		return nil, err
	}
	if err = transferMlsManager(ctx, tx, req); err != nil {
		return nil, err
	}
	envelope := &pb.MlsEnvelope{Sequence: sequence, RequestId: req.RequestId, Epoch: epoch, Kind: req.Kind, Ciphertext: req.Ciphertext, AuthorGrantId: scope.GrantID, AuthorId: scope.PrincipalID, CreatedAtUnixMs: time.Now().UnixMilli(), AddedGrantId: req.AddGrantId, RemovedGrantId: req.RemoveGrantId, Welcome: req.Welcome, NewManagerGrantId: req.NewManagerGrantId}
	if req.Kind == "commit" || req.Kind == "update" {
		envelope.Roster, err = mlsRoster(ctx, tx, req.ChannelId)
		epoch++
		if err != nil {
			return nil, err
		}
	}
	encoded, err := proto.Marshal(envelope)
	if err != nil {
		return nil, status.Error(codes.Internal, "Не удалось сохранить MLS-пакет")
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mls_envelopes(channel_id,sequence,request_id,request_hash,envelope) VALUES($1,$2,$3,$4,$5)", req.ChannelId, sequence, req.RequestId, hash, encoded); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = bindMlsAssets(ctx, tx, req, scope.PrincipalID, sequence); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "UPDATE mls_channels SET epoch=$2,sequence=$3 WHERE channel_id=$1", req.ChannelId, epoch, sequence); err != nil {
		return nil, databaseError(ctx, err)
	}

	return envelope, nil
}
