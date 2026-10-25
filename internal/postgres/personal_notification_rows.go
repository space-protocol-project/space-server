package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

// Доступ к разделу проверяется до запроса. Видимость цели и её текущие
// причины вычисляются заново, включая скрытых родителей и снятые упоминания.
const personalNotificationRows = `WITH visible AS (
 SELECT n.*,v.parent_id,v.sequence AS target_sequence,v.author_name,v.title,v.body,
 ((CASE WHEN v.mentions @> jsonb_build_array(jsonb_build_object('principal_id',n.recipient_id)) THEN n.reasons&1 ELSE 0 END) |
  (CASE WHEN v.reply_recipient=n.recipient_id AND v.author_id<>n.recipient_id THEN n.reasons&2 ELSE 0 END)) AS active_reasons,
 ((CASE WHEN cm.read_at IS NULL THEN 1 ELSE 0 END) |
  (CASE WHEN v.sequence>COALESCE(fr.read_sequence,fe.read_sequence,0) THEN 2 ELSE 0 END)) AS unread_bits
 FROM personal_notifications n JOIN personal_notification_content v
 ON v.channel_id=n.channel_id AND v.kind=n.kind AND v.id=n.content_id
 LEFT JOIN community_mentions cm ON cm.channel_id=n.channel_id AND cm.recipient_id=n.recipient_id AND
 ((n.kind=1 AND cm.forum_thread_id=n.content_id) OR (n.kind=2 AND cm.forum_reply_id=n.content_id) OR
  (n.kind=3 AND cm.feed_post_id=n.content_id) OR (n.kind=4 AND cm.feed_comment_id=n.content_id))
 LEFT JOIN forum_read_state fr ON n.kind=2 AND fr.thread_id=v.parent_id AND fr.principal_id=n.recipient_id
 LEFT JOIN feed_read_state fe ON n.kind=4 AND fe.post_id=v.parent_id AND fe.principal_id=n.recipient_id
 WHERE n.channel_id=$1 AND n.recipient_id=$2
), entries AS (
 SELECT *,CASE WHEN read_at IS NULL THEN active_reasons&unread_bits ELSE 0 END AS unread_reasons FROM visible
)
SELECT id,sequence,channel_id,kind,content_id,parent_id,target_sequence,author_id,author_name,title,body,
 active_reasons,unread_reasons,created_at FROM entries WHERE active_reasons>0`

func readPersonalNotificationRows(ctx context.Context, rows pgx.Rows) ([]*pb.PersonalNotification, error) {
	defer rows.Close()
	out := []*pb.PersonalNotification{}
	for rows.Next() {
		n := new(pb.PersonalNotification)
		var body string
		if err := rows.Scan(&n.Id, &n.Sequence, &n.ChannelId, &n.TargetKind, &n.ContentId, &n.ParentId, &n.TargetSequence,
			&n.AuthorId, &n.AuthorName, &n.Title, &body, &n.Reasons, &n.UnreadReasons, &n.CreatedAtUnix); err != nil {
			return nil, databaseError(ctx, err)
		}
		n.Snippet = communitySnippet(body, "")
		out = append(out, n)
	}
	return out, databaseErrorOrNil(ctx, rows.Err())
}
