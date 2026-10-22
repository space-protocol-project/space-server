package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func checkMlsAssetAccess(ctx context.Context, tx pgx.Tx, channel string) error {
	var encrypted bool
	if err := tx.QueryRow(ctx, "SELECT mls_group_id IS NOT NULL FROM channels WHERE id=$1", channel).Scan(&encrypted); err != nil {
		return databaseError(ctx, err)
	}
	if !encrypted {
		return nil
	}
	scope, err := sessionMlsScope(ctx, tx)
	if err != nil {
		return err
	}
	_, err = activeMlsMember(ctx, tx, channel, scope.GrantID)
	return err
}

func validateUploadMode(ctx context.Context, tx pgx.Tx, req *pb.BeginUploadRequest) error {
	var encrypted bool
	if err := tx.QueryRow(ctx, "SELECT mls_group_id IS NOT NULL FROM channels WHERE id=$1", req.ChannelId).Scan(&encrypted); err != nil {
		return databaseError(ctx, err)
	}
	if encrypted != req.Encrypted || (encrypted && req.Name != "encrypted-file") {
		return status.Error(codes.InvalidArgument, "Для закрытого канала разрешены только зашифрованные файлы без открытого имени")
	}
	return nil
}

func checkPublishedAsset(ctx context.Context, tx pgx.Tx, a *storedAsset) error {
	if !a.ready {
		return status.Error(codes.NotFound, "Файл не опубликован")
	}
	if a.mlsSequence != nil || a.forumThreadID != nil || a.forumReplyID != nil {
		return nil
	}
	if a.contentID == nil {
		return status.Error(codes.NotFound, "Файл не опубликован")
	}
	var deleted bool
	if err := tx.QueryRow(ctx, "SELECT deleted FROM contents WHERE channel_id=$1 AND id=$2", a.asset.ChannelId, *a.contentID).Scan(&deleted); err != nil {
		return databaseError(ctx, err)
	}
	if deleted {
		return status.Error(codes.NotFound, "Сообщение удалено")
	}
	return nil
}
