package postgres

import (
 "context"
 "github.com/jackc/pgx/v5"
 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 "google.golang.org/grpc/codes"
 "google.golang.org/grpc/status"
)

func validateFeedPublication(req *pb.CreateFeedPostRequest) error {
 text := req.Text
 if text == "" && req.RepostOf != "" { text = "repost" }
 return validateForumMessage(text, req.IdempotencyKey, req.AttachmentIds)
}

func validateFeedRepost(ctx context.Context, tx pgx.Tx, channel, id string) error {
 if id == "" { return nil }
 source, err := feedPost(ctx, tx, channel, id)
 if err != nil { return err }
 if source.Hidden { return status.Error(codes.NotFound, "Исходная публикация недоступна") }
 if source.RepostOf != "" { return status.Error(codes.InvalidArgument, "Укажите первоначальную публикацию") }
 return nil
}

func feedReplySequence(ctx context.Context, tx pgx.Tx, post, id string) (int64, error) {
 if id == "" { return 0, nil }
 var sequence int64
 err := tx.QueryRow(ctx, "SELECT sequence FROM feed_comments WHERE post_id=$1 AND id=$2 AND NOT hidden", post, id).Scan(&sequence)
 if err == pgx.ErrNoRows { return 0, status.Error(codes.NotFound, "Комментарий недоступен") }
 return sequence, databaseErrorOrNil(ctx, err)
}

// Раздел уже заблокирован и авторизован feedTransaction. Внешний ключ
// запрещает ссылку в другой раздел; текст оригинала никогда не сохраняется.
func hydrateFeedQuotes(ctx context.Context, tx pgx.Tx, posts []*pb.FeedPost) error {
 ids := make([]string, 0, len(posts))
 for _, post := range posts {
  post.QuotedPost = nil
  if !post.Hidden && post.RepostOf != "" { ids = append(ids, post.RepostOf) }
 }
 if len(ids) == 0 { return nil }
 rows, err := tx.Query(ctx, "SELECT "+feedPostColumns+" FROM feed_posts WHERE id=ANY($1) AND NOT hidden AND repost_of IS NULL", ids)
 if err != nil { return databaseError(ctx, err) }
 defer rows.Close()
 sources := make(map[string]*pb.FeedPost)
 for rows.Next() {
  source, err := scanFeedPost(rows)
  if err != nil { return databaseError(ctx, err) }
  sources[source.Id] = source
 }
 if err = rows.Err(); err != nil { return databaseError(ctx, err) }
 for _, post := range posts {
  source := sources[post.RepostOf]
  if source != nil && source.ChannelId == post.ChannelId { post.QuotedPost = source }
 }
 return nil
}
