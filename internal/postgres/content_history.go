package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

func (s *Store) HistoryPage(ctx context.Context, req *pb.GetHistoryPageRequest) (*pb.GetHistoryPageResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err = s.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	if err = requireOpenChannelAccess(ctx, tx, req.ChannelId, "read"); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id,channel_id,text,author_id,sequence,created_at_unix_ms,revision,updated_at_unix_ms,deleted,attachments,reactions,COALESCE(reply_to_id,'') FROM contents WHERE channel_id=$1 AND ($2::bigint=0 OR sequence<$2) ORDER BY sequence DESC LIMIT 101`, req.ChannelId, req.BeforeSequence)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	contents, err := readContentRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	result := &pb.GetHistoryPageResponse{}
	if len(contents) > 100 {
		contents = contents[:100]
		result.NextBeforeSequence = contents[len(contents)-1].Sequence
	}
	for i := len(contents) - 1; i >= 0; i-- {
		result.Contents = append(result.Contents, contents[i])
	}
	err = tx.QueryRow(ctx, `SELECT cursor FROM events WHERE channel_id=$1 ORDER BY sequence DESC LIMIT 1`, req.ChannelId).Scan(&result.EventCursor)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, databaseError(ctx, err)
	}
	if err = populateContentNames(ctx, tx, result.Contents); err != nil {
		return nil, err
	}
	return result, nil
}
