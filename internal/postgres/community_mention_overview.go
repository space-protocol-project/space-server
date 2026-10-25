package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
)

func overviewMentions(ctx context.Context, tx pgx.Tx, channel string, limit int) ([]*pb.CommunityOverviewEntry, error) {
	rows, err := tx.Query(ctx, `SELECT v.kind,v.id,v.parent_id,v.title,v.body,v.author_id,v.author_name,v.created_at,v.sequence
 FROM community_mentions m JOIN community_mention_content v ON v.channel_id=m.channel_id AND
 ((v.kind=1 AND v.id=m.forum_thread_id) OR (v.kind=2 AND v.id=m.forum_reply_id) OR
  (v.kind=3 AND v.id=m.feed_post_id) OR (v.kind=4 AND v.id=m.feed_comment_id))
 WHERE m.channel_id=$1 AND m.recipient_id=$2 AND m.read_at IS NULL ORDER BY m.id DESC LIMIT $3`, channel, authn.Actor(ctx), limit)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	entries := []*pb.CommunityOverviewEntry{}
	for rows.Next() {
		item := new(pb.CommunitySearchResult)
		var kind int32
		var body string
		if err = rows.Scan(&kind, &item.Id, &item.ParentId, &item.Title, &body, &item.AuthorId, &item.AuthorName, &item.CreatedAtUnix, &item.Sequence); err != nil {
			return nil, databaseError(ctx, err)
		}
		item.Kind = pb.CommunitySearchKind(kind)
		item.Snippet = communitySnippet(body, "")
		entries = append(entries, &pb.CommunityOverviewEntry{Content: item, UnreadCount: 1, ReadStateSupported: true})
	}
	return entries, databaseErrorOrNil(ctx, rows.Err())
}
