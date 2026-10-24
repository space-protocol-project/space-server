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

const overviewForumRows = `SELECT t.id,t.title,t.body,t.author_id,t.author_name,t.created_at,t.sequence,
 (CASE WHEN s.thread_id IS NULL THEN 1 ELSE 0 END)+
 (SELECT count(*) FROM forum_replies r WHERE r.thread_id=t.id AND NOT r.hidden AND r.sequence>COALESCE(s.read_sequence,0)),
 (SELECT count(*) FROM forum_replies r WHERE r.thread_id=t.id AND NOT r.hidden AND r.sequence>COALESCE(s.read_sequence,0) AND r.author_id<>$2 AND t.author_id=$2),
 COALESCE((SELECT min(r.sequence) FROM forum_replies r WHERE r.thread_id=t.id AND NOT r.hidden AND r.sequence>COALESCE(s.read_sequence,0) AND r.author_id<>$2 AND t.author_id=$2),0)
 FROM forum_threads t LEFT JOIN forum_read_state s ON s.thread_id=t.id AND s.principal_id=$2
 WHERE t.channel_id=$1 AND NOT t.hidden`

const overviewFeedRows = `SELECT id,'' AS title,body,author_id,author_name,created_at,sequence,0::bigint,0::bigint,0::bigint
 FROM feed_posts WHERE channel_id=$1 AND NOT hidden ORDER BY sequence DESC LIMIT $2`

func overviewEntries(ctx context.Context, tx pgx.Tx, channel, kind string, limit int, replies bool) ([]*pb.CommunityOverviewEntry, error) {
	query := overviewForumRows + ` ORDER BY t.sequence DESC LIMIT $3`
	args := []any{channel, authn.Actor(ctx), limit}
	contentKind := pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FORUM_THREAD
	if replies {
		// Имена вычисляемых столбцов нужны внешнему фильтру.
		query = `SELECT * FROM (` + overviewForumRows + `) AS entries(id,title,body,author_id,author_name,created_at,sequence,unread,unread_replies,first_reply) WHERE unread_replies>0 ORDER BY created_at DESC,sequence DESC LIMIT $3`
	}
	if kind == "feed" {
		query = overviewFeedRows
		args = []any{channel, limit}
		contentKind = pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FEED_POST
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	entries := []*pb.CommunityOverviewEntry{}
	for rows.Next() {
		entry := &pb.CommunityOverviewEntry{Content: &pb.CommunitySearchResult{Kind: contentKind}, ReadStateSupported: kind == "forum"}
		item := entry.Content
		var body string
		err = rows.Scan(&item.Id, &item.Title, &body, &item.AuthorId, &item.AuthorName, &item.CreatedAtUnix, &item.Sequence, &entry.UnreadCount, &entry.UnreadRepliesToYou, &entry.FirstUnreadReplySequence)
		if err != nil {
			return nil, databaseError(ctx, err)
		}
		item.ParentId = item.Id
		item.Snippet = communitySnippet(body, "")
		entries = append(entries, entry)
	}
	return entries, databaseErrorOrNil(ctx, rows.Err())
}

func overviewReports(ctx context.Context, tx pgx.Tx, channel, kind string) (int64, error) {
	query := `SELECT count(*) FROM forum_reports r JOIN forum_threads t ON t.id=r.thread_id WHERE t.channel_id=$1 AND r.decision=''`
	if kind == "feed" {
		query = `SELECT count(*) FROM feed_reports r JOIN feed_posts p ON p.id=r.post_id WHERE p.channel_id=$1 AND r.decision=''`
	}
	var count int64
	err := tx.QueryRow(ctx, query, channel).Scan(&count)
	return count, databaseErrorOrNil(ctx, err)
}

func (s *Store) GetCommunityOverview(ctx context.Context, req *pb.GetCommunityOverviewRequest) (*pb.GetCommunityOverviewResponse, error) {
	if req.Limit < 0 || req.Limit > 20 {
		return nil, status.Error(codes.InvalidArgument, "Сводка ограничена 20 записями")
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = 6
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, kind, err := s.searchTransaction(ctx, req.ChannelId)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	out := new(pb.GetCommunityOverviewResponse)
	out.Recent, err = overviewEntries(ctx, tx, req.ChannelId, kind, limit, false)
	if err != nil {
		return nil, err
	}
	if kind == "forum" {
		out.RepliesToYou, err = overviewEntries(ctx, tx, req.ChannelId, kind, limit, true)
		if err != nil {
			return nil, err
		}
	}
	channel, err := channelAccess(ctx, tx, req.ChannelId, "read", true)
	if err != nil {
		return nil, err
	}
	out.CanModerate = channel.Permissions.Manage
	if out.CanModerate {
		out.PendingReports, err = overviewReports(ctx, tx, req.ChannelId, kind)
	}
	return out, err
}
