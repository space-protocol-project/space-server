package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Вызывается под блокировкой канала, общей со всеми операциями модерации.
func visibleAssetContentSQL(alias string) string {
	return visibleFeedAssetSQL(alias) + " AND NOT EXISTS(SELECT 1 FROM forum_threads ft WHERE ft.id=" + alias + ".forum_thread_id AND ft.hidden) AND NOT EXISTS(SELECT 1 FROM forum_replies fr JOIN forum_threads ft ON ft.id=fr.thread_id WHERE fr.id=" + alias + ".forum_reply_id AND (fr.hidden OR ft.hidden))"
}
func checkForumAssetVisible(ctx context.Context, tx pgx.Tx, id string) error {
	var visible bool
	err := tx.QueryRow(ctx, "SELECT "+visibleAssetContentSQL("assets")+" FROM assets WHERE id=$1", id).Scan(&visible)
	if err != nil {
		return databaseError(ctx, err)
	}
	if !visible {
		return status.Error(codes.NotFound, "Файл не опубликован")
	}
	return nil
}

func requireAssetCatalogChannel(ctx context.Context, tx pgx.Tx, channel string) error {
	var kind string
	var encrypted bool
	if err := tx.QueryRow(ctx, "SELECT view_type,mls_group_id IS NOT NULL FROM channels WHERE id=$1", channel).Scan(&kind, &encrypted); err != nil {
		return databaseError(ctx, err)
	}
	if encrypted || (kind != "chat" && kind != "forum" && kind != "feed") {
		return status.Error(codes.FailedPrecondition, "Для этого раздела требуется отдельный API")
	}
	return nil
}

func visibleFeedAssetSQL(alias string) string {
	return "NOT EXISTS(SELECT 1 FROM feed_posts fp WHERE fp.id=" + alias + ".feed_post_id AND fp.hidden) AND NOT EXISTS(SELECT 1 FROM feed_comments fc JOIN feed_posts fp ON fp.id=fc.post_id WHERE fc.id=" + alias + ".feed_comment_id AND (fc.hidden OR fp.hidden))"
}
