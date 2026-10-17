package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"strings"
	"time"
)

const searchContentSQL = `SELECT id,channel_id,text,author_id,sequence,created_at_unix_ms,
 revision,updated_at_unix_ms,deleted,attachments,reactions,COALESCE(reply_to_id,'')
 FROM contents WHERE channel_id=$1 AND NOT deleted AND ($2::bigint=0 OR sequence<$2)
 AND space_search_terms_v1(search_text,2,3) @>
 space_search_terms_v1(space_search_fold_v1($3),LEAST(3,char_length($3)),3)
 AND strpos(search_text,space_search_fold_v1($3))>0
 ORDER BY sequence DESC LIMIT 51`

func (s *Store) Search(ctx context.Context, req *pb.SearchContentRequest) (*pb.SearchContentResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
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
	rows, err := tx.Query(ctx, searchContentSQL, req.ChannelId, req.BeforeSequence, strings.TrimSpace(req.Query))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	contents, err := readContentRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	result := &pb.SearchContentResponse{Contents: contents}
	if len(contents) > 50 {
		result.Contents = contents[:50]
		result.NextBeforeSequence = contents[49].Sequence
	}
	if err = populateContentNames(ctx, tx, result.Contents); err != nil {
		return nil, err
	}
	return result, nil
}

func readContentRows(ctx context.Context, rows pgx.Rows) ([]*pb.Content, error) {
	defer rows.Close()
	result := make([]*pb.Content, 0, 51)
	for rows.Next() {
		item := new(pb.Content)
		var files, reactions []byte
		if err := rows.Scan(&item.Id, &item.ChannelId, &item.Text, &item.AuthorId, &item.Sequence, &item.CreatedAtUnixMs, &item.Revision, &item.UpdatedAtUnixMs, &item.Deleted, &files, &reactions, &item.ReplyToId); err != nil {
			return nil, databaseError(ctx, err)
		}
		if err := json.Unmarshal(files, &item.Attachments); err != nil {
			return nil, databaseError(ctx, err)
		}
		if err := json.Unmarshal(reactions, &item.Reactions); err != nil {
			return nil, databaseError(ctx, err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(ctx, err)
	}
	return result, nil
}
