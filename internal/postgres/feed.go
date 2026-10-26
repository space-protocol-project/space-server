package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const feedPostColumns = "id,channel_id,author_id,author_name,body,created_at,sequence,revision,comment_count,attachments,hidden,mentions,COALESCE(repost_of,'')"
const feedCommentColumns = "id,post_id,author_id,author_name,body,created_at,sequence,attachments,hidden,revision,mentions,COALESCE(reply_to_comment_id,''),reply_to_sequence"

func scanFeedPost(row pgx.Row) (*pb.FeedPost, error) {
	t := new(pb.FeedPost)
	var attachments, mentions []byte
	err := row.Scan(&t.Id, &t.ChannelId, &t.AuthorId, &t.AuthorName, &t.Text, &t.CreatedAtUnix, &t.Sequence, &t.Revision, &t.CommentCount, &attachments, &t.Hidden, &mentions, &t.RepostOf)
	if err == nil {
		err = json.Unmarshal(attachments, &t.Attachments)
	}
	if err == nil {
		err = json.Unmarshal(mentions, &t.Mentions)
	}
	return t, err
}

func scanFeedComment(row pgx.Row) (*pb.FeedComment, error) {
	r := new(pb.FeedComment)
	var attachments, mentions []byte
	err := row.Scan(&r.Id, &r.PostId, &r.AuthorId, &r.AuthorName, &r.Text, &r.CreatedAtUnix, &r.Sequence, &attachments, &r.Hidden, &r.Revision, &mentions, &r.ReplyToCommentId, &r.ReplyToSequence)
	if err == nil {
		err = json.Unmarshal(attachments, &r.Attachments)
	}
	if err == nil {
		err = json.Unmarshal(mentions, &r.Mentions)
	}
	return r, err
}

func feedResultError(ctx context.Context, err error) error {
	if err == pgx.ErrNoRows {
		return status.Error(codes.NotFound, "Публикация не найдена")
	}
	return databaseErrorOrNil(ctx, err)
}

func feedPost(ctx context.Context, tx pgx.Tx, channel, id string) (*pb.FeedPost, error) {
	t, err := scanFeedPost(tx.QueryRow(ctx, "SELECT "+feedPostColumns+" FROM feed_posts WHERE channel_id=$1 AND id=$2", channel, id))
	return t, feedResultError(ctx, err)
}

func (s *Store) ListFeedPosts(ctx context.Context, req *pb.ListFeedPostsRequest) (*pb.ListFeedPostsResponse, error) {
	limit, err := forumPage(req.BeforeSequence, req.Limit)
	if err != nil {
		return nil, err
	}
	tx, err := s.feedTransaction(ctx, req.ChannelId, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := feedPostPage(ctx, tx, req, limit+1)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	out := new(pb.ListFeedPostsResponse)
	for rows.Next() {
		t, err := scanFeedPost(rows)
		if err != nil {
			return nil, databaseError(ctx, err)
		}
		if len(out.Posts) == limit {
			out.NextBeforeSequence = out.Posts[limit-1].Sequence

			break
		}
		out.Posts = append(out.Posts, t)
	}
	err = rows.Err()
	rows.Close()
	if err == nil {
		redactFeedPosts(out.Posts)
		err = feedReadStates(ctx, tx, out.Posts)
	}
	if err == nil {
		out.Permissions, err = feedPermissions(ctx, tx, req.ChannelId)
	}
	return out, databaseErrorOrNil(ctx, err)
}

func (s *Store) GetFeedPost(ctx context.Context, req *pb.GetFeedPostRequest) (*pb.GetFeedPostResponse, error) {
	limit, err := forumPage(req.AfterSequence, req.Limit)
	if err != nil {
		return nil, err
	}
	tx, err := s.feedTransaction(ctx, req.ChannelId, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	t, err := feedPost(ctx, tx, req.ChannelId, req.PostId)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, "SELECT "+feedCommentColumns+" FROM feed_comments WHERE post_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3", t.Id, req.AfterSequence, limit+1)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	out := &pb.GetFeedPostResponse{Post: t}
	if t.Hidden {
		rows.Close()
		redactFeedPost(t)
		out.Permissions, err = feedPermissions(ctx, tx, req.ChannelId)
		return out, err
	}
	for rows.Next() {
		r, err := scanFeedComment(rows)
		if err != nil {
			return nil, databaseError(ctx, err)
		}
		if len(out.Comments) == limit {
			out.NextSequence = out.Comments[limit-1].Sequence
			break
		}
		redactFeedComment(r, t.Hidden)
		out.Comments = append(out.Comments, r)
	}
	err = rows.Err()
	rows.Close()
	if err == nil {
		redactFeedPost(t)
		err = feedReadStates(ctx, tx, []*pb.FeedPost{t})
	}
	if err == nil {
		out.Permissions, err = feedPermissions(ctx, tx, req.ChannelId)
	}
	return out, databaseErrorOrNil(ctx, err)
}

func (s *Store) CreateFeedPost(ctx context.Context, req *pb.CreateFeedPostRequest) (*pb.CreateFeedPostResponse, error) {
	if err := validateFeedPublication(req); err != nil {
		return nil, err
	}
	tx, err := s.feedTransaction(ctx, req.ChannelId, "feed.publish")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	old, err := scanFeedPost(tx.QueryRow(ctx, "SELECT "+feedPostColumns+" FROM feed_posts WHERE channel_id=$1 AND author_id=$2 AND idempotency_key=$3", req.ChannelId, authn.Actor(ctx), req.IdempotencyKey))
	if err == nil {
		if old.RepostOf != req.RepostOf || old.Text != req.Text || !forumSameAssets(old.Attachments, req.AttachmentIds) || !sameCommunityMentions(old.Mentions, req.Mentions) {
			return nil, forumKeyConflict()
		}
		redactFeedPosts([]*pb.FeedPost{old})
		if err = feedReadStates(ctx, tx, []*pb.FeedPost{old}); err != nil {
			return nil, err
		}
		return &pb.CreateFeedPostResponse{Post: old}, nil
	}
	if err != pgx.ErrNoRows {
		return nil, databaseError(ctx, err)
	}
	if err = validateFeedRepost(ctx, tx, req.ChannelId, req.RepostOf); err != nil {
		return nil, err
	}
	mentions, err := validateCommunityMentions(ctx, tx, req.ChannelId, req.Text, req.Mentions)
	if err != nil {
		return nil, err
	}
	assets, err := contentAssets(ctx, tx, &pb.CreateContentRequest{ChannelId: req.ChannelId, AttachmentIds: req.AttachmentIds})
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(assets)
	id, err := randomString("post_")
	if err != nil {
		return nil, status.Error(codes.Internal, "Не удалось создать идентификатор")
	}
	name, err := forumAuthorName(ctx, tx)
	if err != nil {
		return nil, err
	}
	var sequence int64
	if err = tx.QueryRow(ctx, "UPDATE channels SET next_sequence=next_sequence+1 WHERE id=$1 RETURNING next_sequence-1", req.ChannelId).Scan(&sequence); err != nil {
		return nil, databaseError(ctx, err)
	}
	t, err := scanFeedPost(tx.QueryRow(ctx, "INSERT INTO feed_posts(id,channel_id,author_id,author_name,body,sequence,idempotency_key,attachments,mentions,repost_of) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')) RETURNING "+feedPostColumns, id, req.ChannelId, authn.Actor(ctx), name, req.Text, sequence, req.IdempotencyKey, encoded, mentions, req.RepostOf))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = bindFeedAssets(ctx, tx, assets, t.Id, true); err != nil {
		return nil, err
	}
	redactFeedPosts([]*pb.FeedPost{t})
	if err = feedReadStates(ctx, tx, []*pb.FeedPost{t}); err != nil {
		return nil, err
	}
	if err = recordCommunityMentions(ctx, tx, req.ChannelId, t.Id, pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FEED_POST, req.Mentions); err != nil {
		return nil, err
	}
	return &pb.CreateFeedPostResponse{Post: t}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}

func (s *Store) CommentFeedPost(ctx context.Context, req *pb.CommentFeedPostRequest) (*pb.CommentFeedPostResponse, error) {
	if err := validateForumMessage(req.Text, req.IdempotencyKey, req.AttachmentIds); err != nil {
		return nil, err
	}
	tx, err := s.feedTransaction(ctx, req.ChannelId, "feed.comment")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	t, err := feedPost(ctx, tx, req.ChannelId, req.PostId)
	if err != nil {
		return nil, err
	}
	old, err := scanFeedComment(tx.QueryRow(ctx, "SELECT "+feedCommentColumns+" FROM feed_comments WHERE channel_id=$1 AND author_id=$2 AND idempotency_key=$3", req.ChannelId, authn.Actor(ctx), req.IdempotencyKey))
	if err == nil {
		if old.ReplyToCommentId != req.ReplyToCommentId || old.PostId != req.PostId || old.Text != req.Text || !forumSameAssets(old.Attachments, req.AttachmentIds) || !sameCommunityMentions(old.Mentions, req.Mentions) {
			return nil, forumKeyConflict()
		}
		redactFeedPosts([]*pb.FeedPost{t})
		if err = feedReadStates(ctx, tx, []*pb.FeedPost{t}); err != nil {
			return nil, err
		}
		redactFeedComment(old, t.Hidden)
		return &pb.CommentFeedPostResponse{Comment: old, Post: t}, nil
	}
	if err != pgx.ErrNoRows {
		return nil, databaseError(ctx, err)
	}
	if t.Hidden {
		return nil, status.Error(codes.FailedPrecondition, "Публикация скрыта")
	}
	replySequence, err := feedReplySequence(ctx, tx, t.Id, req.ReplyToCommentId)
	if err != nil {
		return nil, err
	}
	mentions, err := validateCommunityMentions(ctx, tx, req.ChannelId, req.Text, req.Mentions)
	if err != nil {
		return nil, err
	}
	assets, err := contentAssets(ctx, tx, &pb.CreateContentRequest{ChannelId: req.ChannelId, AttachmentIds: req.AttachmentIds})
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(assets)
	id, err := randomString("comment_")
	if err != nil {
		return nil, status.Error(codes.Internal, "Не удалось создать идентификатор")
	}
	name, err := forumAuthorName(ctx, tx)
	if err != nil {
		return nil, err
	}
	r, err := scanFeedComment(tx.QueryRow(ctx, "INSERT INTO feed_comments(id,channel_id,post_id,author_id,author_name,body,sequence,idempotency_key,attachments,mentions,reply_to_comment_id,reply_to_sequence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''),$12) RETURNING "+feedCommentColumns, id, req.ChannelId, t.Id, authn.Actor(ctx), name, req.Text, t.CommentCount+1, req.IdempotencyKey, encoded, mentions, req.ReplyToCommentId, replySequence))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	t, err = scanFeedPost(tx.QueryRow(ctx, "UPDATE feed_posts SET comment_count=comment_count+1,revision=revision+1 WHERE id=$1 RETURNING "+feedPostColumns, t.Id))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = bindFeedAssets(ctx, tx, assets, r.Id, false); err != nil {
		return nil, err
	}
	redactFeedPosts([]*pb.FeedPost{t})
	if err = feedReadStates(ctx, tx, []*pb.FeedPost{t}); err != nil {
		return nil, err
	}
	if err = recordCommunityMentions(ctx, tx, req.ChannelId, r.Id, pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FEED_COMMENT, req.Mentions); err != nil {
		return nil, err
	}
	return &pb.CommentFeedPostResponse{Comment: r, Post: t}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}

func bindFeedAssets(ctx context.Context, tx pgx.Tx, assets []*pb.Asset, id string, post bool) error {
	column := "feed_comment_id"
	if post {
		column = "feed_post_id"
	}
	for _, a := range assets {
		if _, err := tx.Exec(ctx, "UPDATE assets SET "+column+"=$2 WHERE id=$1", a.Id, id); err != nil {
			return databaseError(ctx, err)
		}
	}
	return nil
}

func feedPostPage(ctx context.Context, tx pgx.Tx, req *pb.ListFeedPostsRequest, limit int) (pgx.Rows, error) {
	return tx.Query(ctx, "SELECT "+feedPostColumns+" FROM feed_posts WHERE channel_id=$1 AND ($2=0 OR sequence<$2) ORDER BY sequence DESC LIMIT $3", req.ChannelId, req.BeforeSequence, limit)
}
func redactFeedPosts(posts []*pb.FeedPost) {
	for _, post := range posts {
		redactFeedPost(post)
	}
}
func redactFeedPost(t *pb.FeedPost) {
	if t.Hidden {
		t.RepostOf = ""
		t.QuotedPost = nil
		t.Text = ""
		t.Mentions = nil
		t.AuthorId = ""
		t.AuthorName = ""
		t.Attachments = nil
		t.CommentCount = 0
		t.ReadStateSupported, t.Seen = false, false
		t.ReadSequence, t.UnreadCount = 0, 0
	}
}
func redactFeedComment(r *pb.FeedComment, parent bool) {
	if r.Hidden || parent {
		r.ReplyToCommentId = ""
		r.ReplyToSequence = 0
		r.Text = ""
		r.Mentions = nil
		r.AuthorId = ""
		r.AuthorName = ""
		r.Attachments = nil
	}
}
