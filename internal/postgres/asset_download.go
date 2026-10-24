package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Store) DownloadChunk(ctx context.Context, req *pb.DownloadChunkRequest) (*pb.DownloadChunkResponse, error) {
	var channel string
	err := s.pool.QueryRow(ctx, "SELECT channel_id FROM assets WHERE id=$1", req.AssetId).Scan(&channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "Файл не найден")
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	tx, err := s.assetTransaction(ctx, channel, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	a, err := readStoredAsset(tx.QueryRow(ctx, `SELECT `+assetColumns+`,received_bytes,ready,content_id,mls_sequence,forum_thread_id,forum_reply_id,feed_post_id,feed_comment_id FROM assets WHERE id=$1 FOR SHARE`, req.AssetId))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "Файл не найден")
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = checkPublishedAsset(ctx, tx, a); err != nil {
		return nil, err
	}
	if req.Offset < 0 || req.Offset >= a.asset.ByteSize || req.Offset%assetChunkSize != 0 {
		return nil, status.Error(codes.InvalidArgument, "Некорректное смещение файла")
	}
	var data []byte
	if err = tx.QueryRow(ctx, "SELECT data FROM asset_chunks WHERE asset_id=$1 AND byte_offset=$2", req.AssetId, req.Offset).Scan(&data); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.DownloadChunkResponse{Asset: a.asset, Data: data, NextOffset: req.Offset + int64(len(data))}, nil
}

func (s *Store) ListMyAssets(ctx context.Context, req *pb.ListMyAssetsRequest) (*pb.ListMyAssetsResponse, error) {
	return s.listAssets(ctx, req.ChannelId, req.After, authn.Actor(ctx))
}

func (s *Store) listAssets(ctx context.Context, channel, after, author string) (*pb.ListMyAssetsResponse, error) {
	tx, err := s.assetTransaction(ctx, channel, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = requireAssetCatalogChannel(ctx, tx, channel); err != nil {
		return nil, err
	}
	var beforeDate int64 = 1<<63 - 1
	beforeID := ""
	if after != "" {
		err = tx.QueryRow(ctx, `SELECT created_at_unix_ms,id FROM assets WHERE `+visibleAssetContentSQL("assets")+` AND id=$1 AND channel_id=$2 AND ($3='' OR author_id=$3) AND (content_id IS NOT NULL OR forum_thread_id IS NOT NULL OR forum_reply_id IS NOT NULL OR feed_post_id IS NOT NULL OR feed_comment_id IS NOT NULL)`, after, channel, author).Scan(&beforeDate, &beforeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.InvalidArgument, "Указатель списка файлов устарел")
		}
		if err != nil {
			return nil, databaseError(ctx, err)
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+assetColumns+`,received_bytes,ready,content_id,mls_sequence,forum_thread_id,forum_reply_id,feed_post_id,feed_comment_id FROM assets WHERE `+visibleAssetContentSQL("assets")+` AND channel_id=$1 AND ($2='' OR author_id=$2) AND ready AND (content_id IS NOT NULL OR forum_thread_id IS NOT NULL OR forum_reply_id IS NOT NULL OR feed_post_id IS NOT NULL OR feed_comment_id IS NOT NULL) AND (created_at_unix_ms,id)<($3,$4) AND (forum_thread_id IS NOT NULL OR forum_reply_id IS NOT NULL OR feed_post_id IS NOT NULL OR feed_comment_id IS NOT NULL OR EXISTS(SELECT 1 FROM contents c WHERE c.channel_id=assets.channel_id AND c.id=assets.content_id AND NOT c.deleted)) ORDER BY created_at_unix_ms DESC,id DESC LIMIT 51`, channel, author, beforeDate, beforeID)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := new(pb.ListMyAssetsResponse)
	for rows.Next() {
		a, readErr := readStoredAsset(rows)
		if readErr != nil {
			return nil, databaseError(ctx, readErr)
		}
		if len(result.Assets) == 50 {
			result.NextCursor = result.Assets[49].Id
			break
		}
		result.Assets = append(result.Assets, a.asset)
	}
	if err = rows.Err(); err != nil {
		return nil, databaseError(ctx, err)
	}
	return result, nil
}
