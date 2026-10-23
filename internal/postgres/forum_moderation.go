package postgres

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Заглушки одинаковы для старых клиентов и модераторов: оригинал доступен после восстановления.
func redactForumThread(t *pb.ForumThread) {
	if t.Hidden {
		t.Title = ""
		t.Text = ""
		t.AuthorId = ""
		t.AuthorName = ""
		t.Attachments = nil
	}
}
func redactForumReply(r *pb.ForumReply, parentHidden bool) {
	if r.Hidden || parentHidden {
		r.Text = ""
		r.AuthorId = ""
		r.AuthorName = ""
		r.Attachments = nil
	}
}
func moderationReason(reason string) error {
	if !validForumText(reason, 1024) {
		return status.Error(codes.InvalidArgument, "Нужна причина до 1024 байт")
	}
	return nil
}
func recordForumModeration(ctx context.Context, tx pgx.Tx, t *pb.ForumThread, reply, action, reason string, revision int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO forum_moderation(thread_id,reply_id,action,reason,actor_id,revision) VALUES($1,$2,$3,$4,$5,$6)`, t.Id, reply, action, strings.TrimSpace(reason), authn.Actor(ctx), revision)
	if err != nil {
		return databaseError(ctx, err)
	}
	target := t.Id
	if reply != "" {
		target = reply
	}
	return databaseErrorOrNil(ctx, audit(ctx, tx, authn.Actor(ctx), "forum."+action+":"+t.ChannelId+":"+target, revision))
}
func threadModerationChanges(t *pb.ForumThread, action string) (bool, bool, error) {
	switch action {
	case "reopen":
		if t.Hidden || !t.Closed {
			return false, false, status.Error(codes.FailedPrecondition, "Сначала восстановите скрытую тему; открыть можно закрытую тему")
		}
		return false, false, nil
	case "hide":
		if t.Hidden {
			return false, false, status.Error(codes.FailedPrecondition, "Тема уже скрыта")
		}
		return true, t.Closed, nil
	case "restore":
		if !t.Hidden {
			return false, false, status.Error(codes.FailedPrecondition, "Тема не скрыта")
		}
		return false, t.Closed, nil
	default:
		return false, false, status.Error(codes.InvalidArgument, "Неизвестное действие модератора")
	}
}
func (s *Store) ModerateForumThread(ctx context.Context, req *pb.ModerateForumThreadRequest) (*pb.ModerateForumThreadResponse, error) {
	if err := moderationReason(req.Reason); err != nil {
		return nil, err
	}
	tx, err := s.forumTransaction(ctx, req.ChannelId, "manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	t, err := forumThread(ctx, tx, req.ChannelId, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if t.Revision != req.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "Тема изменилась; обновите её")
	}
	hidden, closed, err := threadModerationChanges(t, req.Action)
	if err != nil {
		return nil, err
	}
	t, err = scanForumThread(tx.QueryRow(ctx, "UPDATE forum_threads SET hidden=$2,closed=$3,revision=revision+1 WHERE id=$1 RETURNING "+forumThreadColumns, t.Id, hidden, closed))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = recordForumModeration(ctx, tx, t, "", req.Action, req.Reason, t.Revision); err != nil {
		return nil, err
	}
	if err = forumReadStates(ctx, tx, []*pb.ForumThread{t}); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.ModerateForumThreadResponse{Thread: t}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}
func (s *Store) ModerateForumReply(ctx context.Context, req *pb.ModerateForumReplyRequest) (*pb.ModerateForumReplyResponse, error) {
	if err := moderationReason(req.Reason); err != nil {
		return nil, err
	}
	tx, err := s.forumTransaction(ctx, req.ChannelId, "manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	t, err := forumThread(ctx, tx, req.ChannelId, req.ThreadId)
	if err != nil {
		return nil, err
	}
	r, err := scanForumReply(tx.QueryRow(ctx, "SELECT "+forumReplyColumns+" FROM forum_replies WHERE thread_id=$1 AND id=$2", t.Id, req.ReplyId))
	if err != nil {
		return nil, forumResultError(ctx, err)
	}
	if r.Revision != req.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "Ответ изменился; обновите тему")
	}
	if r.Hidden == req.Hidden {
		return nil, status.Error(codes.FailedPrecondition, "Состояние ответа уже изменено")
	}
	r, err = scanForumReply(tx.QueryRow(ctx, "UPDATE forum_replies SET hidden=$2,revision=revision+1 WHERE id=$1 RETURNING "+forumReplyColumns, r.Id, req.Hidden))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	t, err = scanForumThread(tx.QueryRow(ctx, "UPDATE forum_threads SET revision=revision+1 WHERE id=$1 RETURNING "+forumThreadColumns, t.Id))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	action := "restore_reply"
	if req.Hidden {
		action = "hide_reply"
	}
	if err = recordForumModeration(ctx, tx, t, r.Id, action, req.Reason, r.Revision); err != nil {
		return nil, err
	}
	redactForumReply(r, t.Hidden)
	if err = forumReadStates(ctx, tx, []*pb.ForumThread{t}); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.ModerateForumReplyResponse{Reply: r, Thread: t}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}
func (s *Store) ListForumModeration(ctx context.Context, req *pb.ListForumModerationRequest) (*pb.ListForumModerationResponse, error) {
	limit, err := forumPage(req.AfterId, req.Limit)
	if err != nil {
		return nil, err
	}
	tx, err := s.forumTransaction(ctx, req.ChannelId, "manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = forumThread(ctx, tx, req.ChannelId, req.ThreadId); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id,thread_id,reply_id,action,reason,actor_id,revision,created_at_unix,report_id FROM forum_moderation WHERE thread_id=$1 AND id>$2 ORDER BY id LIMIT $3`, req.ThreadId, req.AfterId, limit+1)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	out := new(pb.ListForumModerationResponse)
	for rows.Next() {
		e := new(pb.ForumModerationEvent)
		if err = rows.Scan(&e.Id, &e.ThreadId, &e.ReplyId, &e.Action, &e.Reason, &e.ActorId, &e.Revision, &e.CreatedAtUnix, &e.ReportId); err != nil {
			return nil, databaseError(ctx, err)
		}
		if len(out.Events) == limit {
			out.NextId = out.Events[limit-1].Id
			break
		}
		out.Events = append(out.Events, e)
	}
	return out, databaseErrorOrNil(ctx, rows.Err())
}
