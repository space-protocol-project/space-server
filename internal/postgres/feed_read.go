package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Один запрос для всей страницы; скрытое содержимое не участвует в счётчиках.
func feedReadStates(ctx context.Context, tx pgx.Tx, posts []*pb.FeedPost) error {
	ids := make([]string, 0, len(posts))
	byID := make(map[string]*pb.FeedPost, len(posts))
	for _, post := range posts {
		if !post.Hidden {
			ids = append(ids, post.Id)
			byID[post.Id] = post
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT p.id,s.post_id IS NOT NULL,COALESCE(s.read_sequence,0),
  (CASE WHEN s.post_id IS NULL THEN 1 ELSE 0 END)+
  (SELECT count(*) FROM feed_comments c WHERE c.post_id=p.id AND NOT c.hidden AND c.sequence>COALESCE(s.read_sequence,0))
  FROM feed_posts p LEFT JOIN feed_read_state s ON s.post_id=p.id AND s.principal_id=$2
  WHERE p.id=ANY($1) AND NOT p.hidden`, ids, authn.Actor(ctx))
	if err != nil {
		return databaseError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var seen bool
		var sequence, unread int64
		if err = rows.Scan(&id, &seen, &sequence, &unread); err != nil {
			return databaseError(ctx, err)
		}
		post := byID[id]
		post.ReadStateSupported, post.Seen = true, seen
		post.ReadSequence, post.UnreadCount = sequence, unread
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return databaseError(ctx, err)
	}
	return hydrateFeedQuotes(ctx, tx, posts)
}

func (s *Store) MarkFeedPostRead(ctx context.Context, req *pb.MarkFeedPostReadRequest) (*pb.MarkFeedPostReadResponse, error) {
	if req.ThroughSequence < 0 {
		return nil, status.Error(codes.InvalidArgument, "Некорректная отметка чтения")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.feedTransaction(ctx, req.ChannelId, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	post, err := scanFeedPost(tx.QueryRow(ctx, "SELECT "+feedPostColumns+" FROM feed_posts WHERE channel_id=$1 AND id=$2 FOR SHARE", req.ChannelId, req.PostId))
	if err != nil {
		return nil, feedResultError(ctx, err)
	}
	if post.Hidden {
		return nil, status.Error(codes.NotFound, "Публикация не найдена")
	}
	if err = feedReadStates(ctx, tx, []*pb.FeedPost{post}); err != nil {
		return nil, err
	}
	var last int64
	if err = tx.QueryRow(ctx, "SELECT COALESCE(max(sequence),0) FROM feed_comments WHERE post_id=$1 AND NOT hidden", post.Id).Scan(&last); err != nil {
		return nil, databaseError(ctx, err)
	}
	if req.ThroughSequence > max(last, post.ReadSequence) {
		return nil, status.Error(codes.InvalidArgument, "Нельзя отметить будущие комментарии")
	}
	_, err = tx.Exec(ctx, `INSERT INTO feed_read_state(post_id,principal_id,read_sequence) VALUES($1,$2,$3)
  ON CONFLICT(post_id,principal_id) DO UPDATE SET read_sequence=GREATEST(feed_read_state.read_sequence,EXCLUDED.read_sequence)`, post.Id, authn.Actor(ctx), req.ThroughSequence)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = feedReadStates(ctx, tx, []*pb.FeedPost{post}); err != nil {
		return nil, err
	}
	return &pb.MarkFeedPostReadResponse{Post: post}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}
