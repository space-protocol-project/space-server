package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Ссылка не копирует прежний текст: после удаления видна только заглушка удалённого сообщения.
func validateReplyTarget(ctx context.Context, tx pgx.Tx, channel, id string) error {
	if id == "" {
		return nil
	}
	if len(id) > 128 {
		return status.Error(codes.InvalidArgument, "Слишком длинная ссылка ответа")
	}
	if err := requireOpenChannelAccess(ctx, tx, channel, "read"); err != nil {
		return err
	}
	var found bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM contents WHERE channel_id=$1 AND id=$2)", channel, id).Scan(&found); err != nil {
		return databaseError(ctx, err)
	}
	if !found {
		return status.Error(codes.FailedPrecondition, "Сообщение для ответа недоступно")
	}
	return nil
}
