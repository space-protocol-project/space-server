package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

func recordFeedModeration(ctx context.Context, tx pgx.Tx, t *pb.FeedPost, comment, action, reason string, revision int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO feed_moderation(post_id,comment_id,action,reason,actor_id,revision) VALUES($1,$2,$3,$4,$5,$6)`, t.Id, comment, action, strings.TrimSpace(reason), authn.Actor(ctx), revision)
	if err != nil {
		return databaseError(ctx, err)
	}
	target := t.Id
	if comment != "" {
		target = comment
	}
	return databaseErrorOrNil(ctx, audit(ctx, tx, authn.Actor(ctx), "feed."+action+":"+t.ChannelId+":"+target, revision))
}

func (s *Store) ModerateFeedComment(ctx context.Context, req *pb.ModerateFeedCommentRequest) (*pb.ModerateFeedCommentResponse, error) {
	if err := moderationReason(req.Reason); err != nil {
		return nil, err
	}
	tx, err := s.feedTransaction(ctx, req.ChannelId, "manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	t, err := feedPost(ctx, tx, req.ChannelId, req.PostId)
	if err != nil {
		return nil, err
	}
	r, err := scanFeedComment(tx.QueryRow(ctx, "SELECT "+feedCommentColumns+" FROM feed_comments WHERE post_id=$1 AND id=$2", t.Id, req.CommentId))
	if err != nil {
		return nil, feedResultError(ctx, err)
	}
	if r.Revision != req.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "Ответ изменился; обновите тему")
	}
	if r.Hidden == req.Hidden {
		return nil, status.Error(codes.FailedPrecondition, "Состояние ответа уже изменено")
	}
	r, err = scanFeedComment(tx.QueryRow(ctx, "UPDATE feed_comments SET hidden=$2,revision=revision+1 WHERE id=$1 RETURNING "+feedCommentColumns, r.Id, req.Hidden))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	t, err = scanFeedPost(tx.QueryRow(ctx, "UPDATE feed_posts SET revision=revision+1 WHERE id=$1 RETURNING "+feedPostColumns, t.Id))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	action := "restore_comment"
	if req.Hidden {
		action = "hide_comment"
	}
	if err = recordFeedModeration(ctx, tx, t, r.Id, action, req.Reason, r.Revision); err != nil {
		return nil, err
	}
	redactFeedComment(r, t.Hidden)
	redactFeedPosts([]*pb.FeedPost{t})
	if err = feedReadStates(ctx, tx, []*pb.FeedPost{t}); err != nil {
		return nil, err
	}
	return &pb.ModerateFeedCommentResponse{Comment: r, Post: t}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}

func (s *Store) ListFeedModeration(ctx context.Context, req *pb.ListFeedModerationRequest) (*pb.ListFeedModerationResponse, error) {
	limit, err := forumPage(req.AfterId, req.Limit)
	if err != nil {
		return nil, err
	}
	tx, err := s.feedTransaction(ctx, req.ChannelId, "manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = feedPost(ctx, tx, req.ChannelId, req.PostId); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id,post_id,comment_id,action,reason,actor_id,revision,created_at_unix,report_id FROM feed_moderation WHERE post_id=$1 AND id>$2 ORDER BY id LIMIT $3`, req.PostId, req.AfterId, limit+1)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	out := new(pb.ListFeedModerationResponse)
	for rows.Next() {
		e := new(pb.FeedModerationEvent)
		if err = rows.Scan(&e.Id, &e.PostId, &e.CommentId, &e.Action, &e.Reason, &e.ActorId, &e.Revision, &e.CreatedAtUnix, &e.ReportId); err != nil {
			return nil, databaseError(ctx, err)
		}
		if len(out.Events) == limit {
			out.NextId = out.Events[limit-1].Id
			break
		}
		out.Events = append(out.Events, e)
	}
	return out, databaseErrorOrNil(ctx, rows.Err())
}

func (s *Store) ModerateFeedPost(ctx context.Context, req *pb.ModerateFeedPostRequest) (*pb.ModerateFeedPostResponse, error) {
	tx, err := s.feedTransaction(ctx, req.ChannelId, "manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = moderationReason(req.Reason); err != nil {
		return nil, err
	}
	t, err := feedPost(ctx, tx, req.ChannelId, req.PostId)
	if err != nil {
		return nil, err
	}
	if t.Revision != req.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "Публикация изменилась")
	}
	if t.Hidden == req.Hidden {
		return nil, status.Error(codes.FailedPrecondition, "Состояние уже изменено")
	}
	t, err = scanFeedPost(tx.QueryRow(ctx, "UPDATE feed_posts SET hidden=$2,revision=revision+1 WHERE id=$1 RETURNING "+feedPostColumns, t.Id, req.Hidden))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	action := "restore"
	if req.Hidden {
		action = "hide"
	}
	if err = recordFeedModeration(ctx, tx, t, "", action, req.Reason, t.Revision); err != nil {
		return nil, err
	}
	redactFeedPost(t)
	if err = feedReadStates(ctx, tx, []*pb.FeedPost{t}); err != nil {
		return nil, err
	}
	return &pb.ModerateFeedPostResponse{Post: t}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}
