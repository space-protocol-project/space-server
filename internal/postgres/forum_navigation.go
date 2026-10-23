package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func forumThreadPage(ctx context.Context, tx pgx.Tx, req *pb.ListForumThreadsRequest, limit int) (pgx.Rows, error) {
	if !req.PinnedFirst {
		return tx.Query(ctx, "SELECT "+forumThreadColumns+" FROM forum_threads WHERE channel_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3", req.ChannelId, req.AfterSequence, limit)
	}
	// Два компонента курсора сохраняют границу между закреплёнными и обычными темами.
	return tx.Query(ctx, "SELECT "+forumThreadColumns+" FROM forum_threads WHERE channel_id=$1 AND ($2=0 OR (pinned=$3 AND sequence>$2) OR ($3 AND NOT pinned)) ORDER BY pinned DESC,sequence LIMIT $4", req.ChannelId, req.AfterSequence, req.AfterPinned, limit)
}

func forumReadStates(ctx context.Context, tx pgx.Tx, threads []*pb.ForumThread) error {
	ids := make([]string, 0, len(threads))
	byID := make(map[string]*pb.ForumThread, len(threads))
	for _, t := range threads {
		t.ReadStateSupported = true
		t.UnreadCount = t.ReplyCount + 1
		ids = append(ids, t.Id)
		byID[t.Id] = t
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, "SELECT thread_id,read_sequence FROM forum_read_state WHERE principal_id=$1 AND thread_id=ANY($2)", authn.Actor(ctx), ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var sequence int64
		if err = rows.Scan(&id, &sequence); err != nil {
			return err
		}
		t := byID[id]
		t.Seen = true
		t.ReadSequence = sequence
		t.UnreadCount = max(0, t.ReplyCount-sequence)
	}
	for _, t := range threads {
		redactForumThread(t)
	}
	return rows.Err()
}

func (s *Store) SetForumThreadPinned(ctx context.Context, req *pb.SetForumThreadPinnedRequest) (*pb.SetForumThreadPinnedResponse, error) {
	tx, err := s.forumTransaction(ctx, req.ChannelId, "manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	t, err := forumThread(ctx, tx, req.ChannelId, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if req.ExpectedRevision != t.Revision {
		return nil, status.Error(codes.Aborted, "Тема изменилась; обновите её")
	}
	if req.Pinned != t.Pinned {
		t, err = scanForumThread(tx.QueryRow(ctx, "UPDATE forum_threads SET pinned=$2,revision=revision+1 WHERE id=$1 RETURNING "+forumThreadColumns, t.Id, req.Pinned))
		if err != nil {
			return nil, databaseError(ctx, err)
		}
		action := "forum.unpin:"
		if req.Pinned {
			action = "forum.pin:"
		}
		if err = audit(ctx, tx, authn.Actor(ctx), action+req.ChannelId+":"+t.Id, t.Revision); err != nil {
			return nil, databaseError(ctx, err)
		}
	}
	if err = forumReadStates(ctx, tx, []*pb.ForumThread{t}); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.SetForumThreadPinnedResponse{Thread: t}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}

func (s *Store) MarkForumThreadRead(ctx context.Context, req *pb.MarkForumThreadReadRequest) (*pb.MarkForumThreadReadResponse, error) {
	tx, err := s.forumTransaction(ctx, req.ChannelId, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	t, err := forumThread(ctx, tx, req.ChannelId, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if req.ThroughSequence < 0 || req.ThroughSequence > t.ReplyCount {
		return nil, status.Error(codes.InvalidArgument, "Некорректная отметка чтения")
	}
	_, err = tx.Exec(ctx, `INSERT INTO forum_read_state(thread_id,principal_id,read_sequence) VALUES($1,$2,$3)
 ON CONFLICT(thread_id,principal_id) DO UPDATE SET read_sequence=GREATEST(forum_read_state.read_sequence,EXCLUDED.read_sequence)`, t.Id, authn.Actor(ctx), req.ThroughSequence)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = forumReadStates(ctx, tx, []*pb.ForumThread{t}); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.MarkForumThreadReadResponse{Thread: t}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}
