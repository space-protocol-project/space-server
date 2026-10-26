package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validCommunityReference(ref *pb.CommunityContentReference) bool {
	return ref != nil && ref.Kind >= 1 && ref.Kind <= 4 && validForumText(ref.Id, 128)
}

func (s *Store) communityContentTransaction(ctx context.Context, channel string, write bool) (pgx.Tx, *pb.Channel, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, databaseError(ctx, err)
	}
	if err = s.lockSession(ctx, tx); err != nil {
		tx.Rollback(ctx)
		return nil, nil, err
	}
	action := "read"
	if write {
		action = "community.react"
	}
	c, err := channelAccess(ctx, tx, channel, action, true)
	if err == nil && (len(c.Views) == 0 || (c.Views[0].Type != "forum" && c.Views[0].Type != "feed")) {
		err = status.Error(codes.FailedPrecondition, "Нужен раздел форума или ленты")
	}
	if err == nil && !c.Permissions.Read {
		err = forbidden()
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, nil, err
	}
	return tx, c, nil
}

func reactionWriteAllowed(ctx context.Context, tx pgx.Tx, c *pb.Channel) (bool, error) {
	if c.Archived {
		return false, nil
	}
	if c.Views[0].Type == "forum" {
		return c.Permissions.Write, nil
	}
	p, err := feedPermissions(ctx, tx, c.Id)
	if err != nil {
		return false, err
	}
	return p.Comment, nil
}

// Видимость объекта и его родителя задаётся общей проекцией сообществ.
// Блокировка канала удерживает ACL, закрытие и модерацию до конца транзакции.
func communityContentTarget(ctx context.Context, tx pgx.Tx, c *pb.Channel, ref *pb.CommunityContentReference) (bool, error) {
	forum := ref.Kind <= 2
	if forum != (c.Views[0].Type == "forum") {
		return false, status.Error(codes.InvalidArgument, "Тип объекта не соответствует разделу")
	}
	var parent string
	err := tx.QueryRow(ctx, `SELECT parent_id FROM community_mention_content WHERE channel_id=$1 AND kind=$2 AND id=$3`, c.Id, int32(ref.Kind), ref.Id).Scan(&parent)
	if err == pgx.ErrNoRows {
		return false, status.Error(codes.NotFound, "Объект не найден")
	}
	if err != nil {
		return false, databaseError(ctx, err)
	}
	if !forum {
		return false, nil
	}
	var closed bool
	err = tx.QueryRow(ctx, "SELECT closed FROM forum_threads WHERE channel_id=$1 AND id=$2", c.Id, parent).Scan(&closed)
	return closed, databaseErrorOrNil(ctx, err)
}
