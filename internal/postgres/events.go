package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Store) ListEvents(ctx context.Context, req *pb.ListEventsRequest) (*pb.ListEventsResponse, error) {
	if req.ChannelId != "general" {
		return nil, status.Error(codes.NotFound, "Канал не найден")
	}
	var after int64
	if req.After != "" {
		err := s.pool.QueryRow(ctx, "SELECT sequence FROM events WHERE channel_id=$1 AND cursor=$2", req.ChannelId, req.After).Scan(&after)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.InvalidArgument, "Неизвестный курсор событий")
		}
		if err != nil {
			return nil, databaseError(ctx, err)
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT e.cursor,e.type,c.id,c.channel_id,c.text,c.author_id
 FROM events e JOIN contents c USING(channel_id,sequence)
 WHERE e.channel_id=$1 AND e.sequence>$2 ORDER BY e.sequence LIMIT 100`, req.ChannelId, after)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := &pb.ListEventsResponse{NextCursor: req.After}
	for rows.Next() {
		event := &pb.Event{Content: new(pb.Content)}
		if err := rows.Scan(&event.Cursor, &event.Type, &event.Content.Id, &event.Content.ChannelId, &event.Content.Text, &event.Content.AuthorId); err != nil {
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
