package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"time"
)

func invalidNotificationPage() error {
	return status.Error(codes.InvalidArgument, "Некорректная страница уведомлений")
}

func (s *Store) notificationTransaction(ctx context.Context, channel string) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = s.lockSession(ctx, tx); err == nil {
		var c *pb.Channel
		c, err = channelAccess(ctx, tx, channel, "read", true)
		if err == nil {
			kind := c.Views[0].Type
			if !c.Archived && len(c.MlsGroupId) == 0 && (kind == "chat" || kind == "forum" || kind == "feed") {
				return tx, nil
			}
			err = status.Error(codes.FailedPrecondition, "Уведомления недоступны в этом разделе")
		}
	}
	tx.Rollback(ctx)
	return nil, err
}

func notificationBounds(ctx context.Context, tx pgx.Tx, req *pb.ListPersonalNotificationsRequest) (int64, error) {
	var latest int64
	err := tx.QueryRow(ctx, "SELECT sequence FROM personal_notification_streams WHERE channel_id=$1 AND recipient_id=$2", req.ChannelId, authn.Actor(ctx)).Scan(&latest)
	if err != nil && err != pgx.ErrNoRows {
		return 0, databaseError(ctx, err)
	}
	through := req.ThroughSequence
	if through == 0 {
		through = latest
	}
	if through > latest || req.AfterSequence > through {
		return 0, invalidNotificationPage()
	}
	return through, nil
}

func (s *Store) ListPersonalNotifications(ctx context.Context, req *pb.ListPersonalNotificationsRequest) (*pb.ListPersonalNotificationsResponse, error) {
	if req.AfterSequence < 0 || req.ThroughSequence < 0 || req.Limit < 0 || req.Limit > 50 {
		return nil, invalidNotificationPage()
	}
	if req.BaselineOnly && (req.AfterSequence != 0 || req.ThroughSequence != 0) {
		return nil, invalidNotificationPage()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.notificationTransaction(ctx, req.ChannelId)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	through, err := notificationBounds(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	out := &pb.ListPersonalNotificationsResponse{NextSequence: through, ThroughSequence: through}
	if req.BaselineOnly {
		return out, nil
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = 50
	}
	rows, err := tx.Query(ctx, personalNotificationRows+` AND sequence>$3 AND sequence<=$4 AND (NOT $5 OR unread_reasons>0) ORDER BY sequence LIMIT $6`, req.ChannelId, authn.Actor(ctx), req.AfterSequence, through, req.UnreadOnly, limit+1)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	out.Notifications, err = readPersonalNotificationRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	if len(out.Notifications) > limit {
		out.Notifications = out.Notifications[:limit]
		out.HasMore = true
		out.NextSequence = out.Notifications[limit-1].Sequence
	}
	return out, nil
}

func notificationIDs(ids []string) (map[string]bool, error) {
	if len(ids) == 0 || len(ids) > 50 {
		return nil, invalidNotificationPage()
	}
	unique := map[string]bool{}
	for _, id := range ids {
		if !validForumText(id, 128) {
			return nil, invalidNotificationPage()
		}
		unique[id] = true
	}
	return unique, nil
}

func (s *Store) MarkPersonalNotificationsRead(ctx context.Context, req *pb.MarkPersonalNotificationsReadRequest) (*pb.MarkPersonalNotificationsReadResponse, error) {
	selector, refs, err := notificationReadSelector(req)
	if err != nil {
		return nil, err
	}
	ids, err := notificationIDs(refs)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.notificationTransaction(ctx, req.ChannelId)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, personalNotificationRows+selector, req.ChannelId, authn.Actor(ctx), refs)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	entries, err := readPersonalNotificationRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	if len(entries) != len(ids) {
		return nil, status.Error(codes.NotFound, "Уведомление недоступно")
	}
	resolved := make([]string, 0, len(entries))
	for _, n := range entries {
		resolved = append(resolved, n.Id)
	}
	if err = markPersonalNotifications(ctx, tx, req.ChannelId, resolved, entries); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.MarkPersonalNotificationsReadResponse{}, nil
}

func markPersonalNotifications(ctx context.Context, tx pgx.Tx, channel string, ids []string, entries []*pb.PersonalNotification) error {
	_, err := tx.Exec(ctx, "UPDATE personal_notifications SET read_at=extract(epoch FROM now())::bigint WHERE channel_id=$1 AND recipient_id=$2 AND id=ANY($3) AND read_at IS NULL", channel, authn.Actor(ctx), ids)
	if err != nil {
		return databaseError(ctx, err)
	}
	for _, n := range entries {
		column := mentionColumn(pb.CommunitySearchKind(n.TargetKind))
		if column == "" {
			continue
		}
		_, err = tx.Exec(ctx, "UPDATE community_mentions SET read_at=extract(epoch FROM now())::bigint WHERE channel_id=$1 AND recipient_id=$2 AND "+column+"=$3 AND read_at IS NULL", channel, authn.Actor(ctx), n.ContentId)
		if err != nil {
			return databaseError(ctx, err)
		}
	}
	return nil
}

func notificationReadSelector(req *pb.MarkPersonalNotificationsReadRequest) (string, []string, error) {
	if len(req.ChatContentIds) > 0 {
		if len(req.Ids) > 0 {
			return "", nil, invalidNotificationPage()
		}
		return " AND kind=5 AND content_id=ANY($3)", req.ChatContentIds, nil
	}
	return " AND id=ANY($3)", req.Ids, nil
}
