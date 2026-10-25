package postgres

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Store) mentionCandidateTransaction(ctx context.Context, channel, parent string) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = s.lockSession(ctx, tx); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	c, err := channelAccess(ctx, tx, channel, "read", true)
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	kind := c.Views[0].Type
	if c.Archived || (kind != "forum" && kind != "feed" && kind != "chat") {
		err = status.Error(codes.FailedPrecondition, "Раздел недоступен для упоминаний")
	}
	if err == nil && (kind == "forum" || kind == "chat") && !c.Permissions.Write {
		err = forbidden()
	}
	if err == nil && kind == "chat" && parent != "" {
		err = invalidMention()
	}
	if err == nil && kind == "feed" {
		action := "feed.publish"
		if parent != "" {
			action = "feed.comment"
		}
		err = requireFeedAction(ctx, tx, c, action)
	}
	if err == nil && parent != "" {
		err = mentionParent(ctx, tx, channel, parent, kind)
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func mentionParent(ctx context.Context, tx pgx.Tx, channel, parent, kind string) error {
	if kind == "forum" {
		topic, err := forumThread(ctx, tx, channel, parent)
		if err != nil {
			return err
		}
		if topic.Hidden {
			return status.Error(codes.NotFound, "Тема не найдена")
		}
		if topic.Closed {
			return status.Error(codes.FailedPrecondition, "Тема закрыта")
		}
		return nil
	}
	post, err := feedPost(ctx, tx, channel, parent)
	if err == nil && post.Hidden {
		return status.Error(codes.NotFound, "Публикация не найдена")
	}
	return err
}

func (s *Store) ListMentionCandidates(ctx context.Context, req *pb.ListMentionCandidatesRequest) (*pb.ListMentionCandidatesResponse, error) {
	query := strings.TrimSpace(req.Query)
	if !utf8.ValidString(query) || len(query) > 128 || strings.ContainsRune(query, 0) {
		return nil, invalidMention()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.mentionCandidateTransaction(ctx, req.ChannelId, req.ParentId)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT m.principal_id,m.nickname"+mentionMembers+` AND m.principal_id<>$3
 AND strpos(space_search_fold_v1(m.nickname||' '||m.principal_id),space_search_fold_v1($2))>0
 ORDER BY m.nickname,m.principal_id LIMIT 20`, req.ChannelId, query, authn.Actor(ctx))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	out := new(pb.ListMentionCandidatesResponse)
	for rows.Next() {
		item := new(pb.MentionCandidate)
		if err = rows.Scan(&item.PrincipalId, &item.DisplayName); err != nil {
			return nil, databaseError(ctx, err)
		}
		item.DisplayName = mentionName(item.DisplayName, item.PrincipalId)
		out.Candidates = append(out.Candidates, item)
	}
	return out, databaseErrorOrNil(ctx, rows.Err())
}

func (s *Store) MarkCommunityMentionsRead(ctx context.Context, req *pb.MarkCommunityMentionsReadRequest) (*pb.MarkCommunityMentionsReadResponse, error) {
	if len(req.Contents) == 0 || len(req.Contents) > 50 {
		return nil, invalidMention()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, _, err := s.searchTransaction(ctx, req.ChannelId)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	for _, ref := range req.Contents {
		column := mentionColumn(ref.GetKind())
		if column == "" || !validForumText(ref.GetId(), 128) {
			return nil, invalidMention()
		}
		var visible bool
		err = tx.QueryRow(ctx, "SELECT true FROM community_mention_content WHERE channel_id=$1 AND kind=$2 AND id=$3", req.ChannelId, int32(ref.Kind), ref.Id).Scan(&visible)
		if err == pgx.ErrNoRows {
			return nil, status.Error(codes.NotFound, "Объект не найден")
		}
		if err != nil {
			return nil, databaseError(ctx, err)
		}
		_, err = tx.Exec(ctx, "UPDATE community_mentions SET read_at=extract(epoch FROM now())::bigint WHERE channel_id=$1 AND recipient_id=$2 AND "+column+"=$3 AND read_at IS NULL", req.ChannelId, authn.Actor(ctx), ref.Id)
		if err != nil {
			return nil, databaseError(ctx, err)
		}
	}
	return &pb.MarkCommunityMentionsReadResponse{}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}
