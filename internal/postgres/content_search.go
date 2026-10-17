package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"strings"
	"time"
	"unicode"
)

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
	rows, err := tx.Query(ctx, `SELECT id,channel_id,text,author_id,sequence,created_at_unix_ms,revision,updated_at_unix_ms,deleted,attachments,reactions,COALESCE(reply_to_id,'') FROM contents WHERE channel_id=$1 AND NOT deleted AND ($2::bigint=0 OR sequence<$2) AND strpos(translate(text,$4,$5),$3)>0 ORDER BY sequence DESC LIMIT 51`, req.ChannelId, req.BeforeSequence, strings.ToLower(strings.TrimSpace(req.Query)), searchUpper, searchLower)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	contents, err := searchContents(ctx, rows)
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

func searchContents(ctx context.Context, rows pgx.Rows) ([]*pb.Content, error) {
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

// Не полагаемся на локаль PostgreSQL: домашняя база может использовать C.
var searchUpper, searchLower = searchCaseAlphabet()

func searchCaseAlphabet() (string, string) {
	var upper, lower strings.Builder
	for value := rune(0); value <= unicode.MaxRune; value++ {
		mapped := unicode.ToLower(value)
		if value != mapped {
			upper.WriteRune(value)
			lower.WriteRune(mapped)
		}
	}
	return upper.String(), lower.String()
}
