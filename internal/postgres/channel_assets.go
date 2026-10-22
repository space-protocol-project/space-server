package postgres

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

func (s *Store) ListChannelAssets(ctx context.Context, req *pb.ListChannelAssetsRequest) (*pb.ListChannelAssetsResponse, error) {
	files, err := s.listAssets(ctx, req.ChannelId, req.After, req.AuthorId)
	if err != nil {
		return nil, err
	}
	tx, err := s.assetTransaction(ctx, req.ChannelId, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT DISTINCT a.author_id,COALESCE(m.nickname,'') FROM assets a LEFT JOIN memberships m ON m.principal_id=a.author_id WHERE a.channel_id=$1 AND a.ready AND (a.forum_thread_id IS NOT NULL OR a.forum_reply_id IS NOT NULL OR (a.content_id IS NOT NULL AND EXISTS(SELECT 1 FROM contents c WHERE c.channel_id=a.channel_id AND c.id=a.content_id AND NOT c.deleted))) ORDER BY a.author_id`, req.ChannelId)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := &pb.ListChannelAssetsResponse{Assets: files.Assets, NextCursor: files.NextCursor, Authors: map[string]string{}}
	for rows.Next() {
		var id, nickname string
		if err = rows.Scan(&id, &nickname); err != nil {
			return nil, databaseError(ctx, err)
		}
		result.Authors[id] = nickname
	}
	if err = rows.Err(); err != nil {
		return nil, databaseError(ctx, err)
	}
	return result, nil
}
