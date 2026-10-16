package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Store) ListEvents(ctx context.Context, req *pb.ListEventsRequest) (*pb.ListEventsResponse, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err := s.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	if _, err := channelAccess(ctx, tx, req.ChannelId, "read", true); err != nil {
		return nil, err
	}
	var after int64
	if req.After != "" {
		err := tx.QueryRow(ctx, "SELECT sequence FROM events WHERE channel_id=$1 AND cursor=$2", req.ChannelId, req.After).Scan(&after)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.InvalidArgument, "Неизвестный курсор событий")
		}
		if err != nil {
			return nil, databaseError(ctx, err)
		}
	}
	rows, err := tx.Query(ctx, `SELECT e.cursor,e.type,c.id,c.channel_id,c.text,c.author_id,c.sequence,c.created_at_unix_ms,c.revision,c.updated_at_unix_ms,c.deleted,c.attachments,c.reactions
 FROM events e JOIN contents c ON c.channel_id=e.channel_id AND c.sequence=e.content_sequence
 WHERE e.channel_id=$1 AND e.sequence>$2 ORDER BY e.sequence LIMIT 100`, req.ChannelId, after)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := &pb.ListEventsResponse{NextCursor: req.After}
	for rows.Next() {
		event := &pb.Event{Content: new(pb.Content)}
		var metadata, reactionData []byte
		if err := rows.Scan(&event.Cursor, &event.Type, &event.Content.Id, &event.Content.ChannelId, &event.Content.Text, &event.Content.AuthorId, &event.Content.Sequence, &event.Content.CreatedAtUnixMs, &event.Content.Revision, &event.Content.UpdatedAtUnixMs, &event.Content.Deleted, &metadata, &reactionData); err != nil {
			return nil, databaseError(ctx, err)
		}
		if err = json.Unmarshal(metadata, &event.Content.Attachments); err != nil {
			return nil, databaseError(ctx, err)
		}
		if err = json.Unmarshal(reactionData, &event.Content.Reactions); err != nil {
			return nil, databaseError(ctx, err)
		}
		result.Events = append(result.Events, event)
		result.NextCursor = event.Cursor
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(ctx, err)
	}
	return result, nil
}
