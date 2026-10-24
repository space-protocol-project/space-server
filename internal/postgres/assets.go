package postgres

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const assetColumns = `id,channel_id,author_id,name,byte_size,sha256,media_type,created_at_unix_ms,storage_group`

type storedAsset struct {
	asset         *pb.Asset
	received      int64
	ready         bool
	contentID     *string
	feedPostID    *string
	feedCommentID *string
	forumThreadID *string
	forumReplyID  *string
	mlsSequence   *int64
}

func readStoredAsset(row pgx.Row) (*storedAsset, error) {
	a := &storedAsset{asset: new(pb.Asset)}
	p := a.asset
	err := row.Scan(&p.Id, &p.ChannelId, &p.AuthorId, &p.Name, &p.ByteSize, &p.Sha256, &p.MediaType, &p.CreatedAtUnixMs, &p.StorageGroup, &a.received, &a.ready, &a.contentID, &a.mlsSequence, &a.forumThreadID, &a.forumReplyID, &a.feedPostID, &a.feedCommentID)
	return a, err
}

func lockAssetQuota(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616003)")
	return err
}

func (s *Store) assetTransaction(ctx context.Context, channel, permission string) (pgx.Tx, error) {
	if authn.Token(ctx) == "" {
		return nil, status.Error(codes.Unauthenticated, "Войдите перед работой с файлами")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = s.lockSession(ctx, tx); err == nil {
		var c *pb.Channel
		var feed bool
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM channels WHERE id=$1 AND view_type='feed')", channel).Scan(&feed)
		action := permission
		if feed && permission == "write" {
			action = "feed.upload"
		}
		if err == nil {
			c, err = channelAccess(ctx, tx, channel, action, true)
		}
		if err == nil && feed {
			err = requireFeedAction(ctx, tx, c, action)
		}
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	if err = checkMlsAssetAccess(ctx, tx, channel); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	if err = requireAttachmentsEnabled(ctx, tx); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func reserveAssetSpace(ctx context.Context, tx pgx.Tx, size int64) error {
	if err := lockAssetQuota(ctx, tx); err != nil {
		return databaseError(ctx, err)
	}
	// Неопубликованные загрузки хранятся не более суток.
	_, err := tx.Exec(ctx, `DELETE FROM assets WHERE content_id IS NULL AND mls_sequence IS NULL AND forum_thread_id IS NULL AND forum_reply_id IS NULL AND feed_post_id IS NULL AND feed_comment_id IS NULL AND created_at_unix_ms < $1`, time.Now().Add(-24*time.Hour).UnixMilli())
	if err != nil {
		return databaseError(ctx, err)
	}
	var used, count, pending, personal, totalLimit, personalLimit int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(sum(byte_size),0),COALESCE(sum(byte_size) FILTER (WHERE author_id=$1),0),count(*),count(*) FILTER (WHERE author_id=$1 AND content_id IS NULL AND mls_sequence IS NULL AND forum_thread_id IS NULL AND forum_reply_id IS NULL AND feed_post_id IS NULL AND feed_comment_id IS NULL) FROM assets`, authn.Actor(ctx)).Scan(&used, &personal, &count, &pending)
	if err != nil {
		return databaseError(ctx, err)
	}
	if err = tx.QueryRow(ctx, "SELECT s.storage_limit_bytes,COALESCE((SELECT m.storage_limit_bytes FROM memberships m WHERE m.principal_id=$1),s.member_storage_limit_bytes) FROM space_settings s WHERE singleton=true", authn.Actor(ctx)).Scan(&totalLimit, &personalLimit); err != nil {
		return databaseError(ctx, err)
	}
	if used+size > totalLimit || personal+size > personalLimit || count >= 10000 || pending >= 20 {
		return status.Error(codes.ResourceExhausted, "Недостаточно места или слишком много незавершённых загрузок")
	}
	return nil
}

func (s *Store) BeginUpload(ctx context.Context, req *pb.BeginUploadRequest) (*pb.BeginUploadResponse, error) {
	if err := validateUpload(req); err != nil {
		return nil, err
	}
	tx, err := s.assetTransaction(ctx, req.ChannelId, "write")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = validateUploadMode(ctx, tx, req); err != nil {
		return nil, err
	}
	previous, err := readStoredAsset(tx.QueryRow(ctx, `SELECT `+assetColumns+`,received_bytes,ready,content_id,mls_sequence,forum_thread_id,forum_reply_id,feed_post_id,feed_comment_id FROM assets WHERE channel_id=$1 AND author_id=$2 AND idempotency_key=$3`, req.ChannelId, authn.Actor(ctx), req.IdempotencyKey))
	if err == nil {
		if previous.forumThreadID != nil || previous.forumReplyID != nil || previous.feedPostID != nil || previous.feedCommentID != nil {
			if err = checkPublishedAsset(ctx, tx, previous); err != nil {
				return nil, err
			}
		}
		return replayAssetUpload(previous, req)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, databaseError(ctx, err)
	}
	if err = reserveAssetSpace(ctx, tx, req.ByteSize); err != nil {
		return nil, err
	}
	id, err := randomString("asset_")
	if err != nil {
		return nil, status.Error(codes.Internal, "Не удалось создать идентификатор файла")
	}
	now := time.Now().UTC()
	a := &pb.Asset{Id: id, ChannelId: req.ChannelId, AuthorId: authn.Actor(ctx), Name: req.Name, ByteSize: req.ByteSize, Sha256: req.Sha256, MediaType: "application/octet-stream", CreatedAtUnixMs: now.UnixMilli(), StorageGroup: authn.Actor(ctx) + "/" + now.Format("2006/01/02")}
	_, err = tx.Exec(ctx, `INSERT INTO assets(id,channel_id,author_id,name,byte_size,sha256,created_at_unix_ms,storage_group,idempotency_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, a.Id, a.ChannelId, a.AuthorId, a.Name, a.ByteSize, a.Sha256, a.CreatedAtUnixMs, a.StorageGroup, req.IdempotencyKey)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.BeginUploadResponse{Asset: a, ChunkSize: assetChunkSize}, nil
}

func replayAssetUpload(a *storedAsset, req *pb.BeginUploadRequest) (*pb.BeginUploadResponse, error) {
	if a.asset.Name != req.Name || a.asset.ByteSize != req.ByteSize || !bytes.Equal(a.asset.Sha256, req.Sha256) {
		return nil, status.Error(codes.AlreadyExists, "Ключ загрузки уже использован для другого файла")
	}
	return &pb.BeginUploadResponse{Asset: a.asset, ReceivedBytes: a.received, ChunkSize: assetChunkSize}, nil
}

func (s *Store) ownedAssetTransaction(ctx context.Context, id string) (pgx.Tx, *storedAsset, error) {
	var channel string
	err := s.pool.QueryRow(ctx, "SELECT channel_id FROM assets WHERE id=$1", id).Scan(&channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, status.Error(codes.NotFound, "Файл не найден")
	}
	if err != nil {
		return nil, nil, databaseError(ctx, err)
	}
	tx, err := s.assetTransaction(ctx, channel, "write")
	if err != nil {
		return nil, nil, err
	}
	a, err := readStoredAsset(tx.QueryRow(ctx, `SELECT `+assetColumns+`,received_bytes,ready,content_id,mls_sequence,forum_thread_id,forum_reply_id,feed_post_id,feed_comment_id FROM assets WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = status.Error(codes.NotFound, "Файл не найден")
	}
	if err == nil && (a.forumThreadID != nil || a.forumReplyID != nil || a.feedPostID != nil || a.feedCommentID != nil) {
		err = checkPublishedAsset(ctx, tx, a)
	}
	if err == nil && a.asset.AuthorId != authn.Actor(ctx) {
		err = status.Error(codes.PermissionDenied, "Изменять загрузку может только автор")
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, nil, err
	}
	return tx, a, nil
}

func (s *Store) UploadChunk(ctx context.Context, req *pb.UploadChunkRequest) (*pb.UploadChunkResponse, error) {
	tx, a, err := s.ownedAssetTransaction(ctx, req.AssetId)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = validateAssetChunk(req.Offset, req.Data, a.asset.ByteSize); err != nil {
		return nil, err
	}
	if req.Offset < a.received {
		return replayAssetChunk(ctx, tx, a, req)
	}
	if a.ready || a.contentID != nil || a.mlsSequence != nil || a.forumThreadID != nil || a.forumReplyID != nil || a.feedPostID != nil || a.feedCommentID != nil || req.Offset != a.received {
		return nil, status.Error(codes.FailedPrecondition, "Обновите состояние загрузки")
	}
	_, err = tx.Exec(ctx, `INSERT INTO asset_chunks(asset_id,byte_offset,data) VALUES($1,$2,$3)`, req.AssetId, req.Offset, req.Data)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	a.received += int64(len(req.Data))
	_, err = tx.Exec(ctx, "UPDATE assets SET received_bytes=$2 WHERE id=$1", req.AssetId, a.received)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.UploadChunkResponse{ReceivedBytes: a.received}, nil
}

func replayAssetChunk(ctx context.Context, tx pgx.Tx, a *storedAsset, req *pb.UploadChunkRequest) (*pb.UploadChunkResponse, error) {
	var previous []byte
	if err := tx.QueryRow(ctx, "SELECT data FROM asset_chunks WHERE asset_id=$1 AND byte_offset=$2", req.AssetId, req.Offset).Scan(&previous); err != nil {
		return nil, databaseError(ctx, err)
	}
	if !bytes.Equal(previous, req.Data) {
		return nil, status.Error(codes.AlreadyExists, "Эта часть уже загружена с другими данными")
	}
	return &pb.UploadChunkResponse{ReceivedBytes: a.received}, nil
}

func (s *Store) CancelUpload(ctx context.Context, req *pb.CancelUploadRequest) (*pb.CancelUploadResponse, error) {
	tx, a, err := s.ownedAssetTransaction(ctx, req.AssetId)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if a.contentID != nil || a.mlsSequence != nil || a.forumThreadID != nil || a.forumReplyID != nil || a.feedPostID != nil || a.feedCommentID != nil {
		return nil, status.Error(codes.FailedPrecondition, "Файл уже прикреплён к сообщению")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM assets WHERE id=$1", a.asset.Id); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CancelUploadResponse{}, nil
}

func requireAttachmentsEnabled(ctx context.Context, tx pgx.Tx) error {
	var enabled bool
	if err := tx.QueryRow(ctx, "SELECT attachments_enabled FROM space_settings WHERE singleton=true FOR SHARE").Scan(&enabled); err != nil {
		return databaseError(ctx, err)
	}
	if !enabled {
		return status.Error(codes.PermissionDenied, "Вложения отключены администратором")
	}
	return nil
}
