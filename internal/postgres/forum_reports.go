package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const reportColumns = "r.id,r.thread_id,r.reply_id,r.reason,r.decision,r.decision_reason,r.moderator_id,r.created_at_unix,r.resolved_at_unix,(SELECT CASE WHEN ft.hidden THEN '' ELSE ft.title END FROM forum_threads ft WHERE ft.id=r.thread_id)"

func scanForumReport(row pgx.Row) (*pb.ForumReport, error) {
	r := new(pb.ForumReport)
	err := row.Scan(&r.Id, &r.ThreadId, &r.ReplyId, &r.Reason, &r.Decision, &r.DecisionReason, &r.ModeratorId, &r.CreatedAtUnix, &r.ResolvedAtUnix, &r.ThreadTitle)
	return r, err
}

func reportTarget(ctx context.Context, tx pgx.Tx, t *pb.ForumThread, reply string) (int64, bool, error) {
	if reply == "" {
		return t.Revision, t.Hidden, nil
	}
	var revision int64
	var hidden bool
	err := tx.QueryRow(ctx, "SELECT revision,hidden FROM forum_replies WHERE thread_id=$1 AND id=$2", t.Id, reply).Scan(&revision, &hidden)
	return revision, hidden || t.Hidden, forumResultError(ctx, err)
}

func reportAllowance(ctx context.Context, tx pgx.Tx, thread, reply string) error {
	var duplicate bool
	var recent int
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM forum_reports WHERE reporter_id=$1 AND thread_id=$2 AND reply_id=$3),
 (SELECT count(*) FROM forum_reports WHERE reporter_id=$1 AND created_at_unix>extract(epoch FROM now())::bigint-3600)`, authn.Actor(ctx), thread, reply).Scan(&duplicate, &recent)
	if err != nil {
		return databaseError(ctx, err)
	}
	if !duplicate && recent >= 30 {
		return status.Error(codes.ResourceExhausted, "Слишком много жалоб. Попробуйте позже")
	}
	return nil
}

func (s *Store) ReportForumContent(ctx context.Context, req *pb.ReportForumContentRequest) (*pb.ReportForumContentResponse, error) {
	if err := moderationReason(req.Reason); err != nil {
		return nil, err
	}
	tx, err := s.forumTransaction(ctx, req.ChannelId, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Жалоба доступна читателю, но не в архивном разделе.
	var archived bool
	if err = tx.QueryRow(ctx, "SELECT archived FROM channels WHERE id=$1", req.ChannelId).Scan(&archived); err != nil {
		return nil, databaseError(ctx, err)
	}
	if archived {
		return nil, status.Error(codes.FailedPrecondition, "Раздел архивирован")
	}
	t, err := forumThread(ctx, tx, req.ChannelId, req.ThreadId)
	if err != nil {
		return nil, err
	}
	_, hidden, err := reportTarget(ctx, tx, t, req.ReplyId)
	if err != nil {
		return nil, err
	}
	if hidden {
		return nil, status.Error(codes.NotFound, "Содержимое недоступно")
	}
	// Блокировка участника сериализует лимит также между разными разделами.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "forum-report:"+authn.Actor(ctx)); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = reportAllowance(ctx, tx, t.Id, req.ReplyId); err != nil {
		return nil, err
	}
	// Одна жалоба от участника на объект; повтор запроса не создаёт дубль.
	_, err = tx.Exec(ctx, `INSERT INTO forum_reports(thread_id,reply_id,reporter_id,reason) VALUES($1,$2,$3,$4) ON CONFLICT(thread_id,reply_id,reporter_id) DO NOTHING`, t.Id, req.ReplyId, authn.Actor(ctx), strings.TrimSpace(req.Reason))
	if err == nil {
		err = tx.Commit(ctx)
	}
	return &pb.ReportForumContentResponse{}, databaseErrorOrNil(ctx, err)
}

func (s *Store) ListForumReports(ctx context.Context, req *pb.ListForumReportsRequest) (*pb.ListForumReportsResponse, error) {
	limit, err := forumPage(req.AfterId, req.Limit)
	if err != nil {
		return nil, err
	}
	tx, err := s.forumTransaction(ctx, req.ChannelId, "manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+reportColumns+` FROM forum_reports r JOIN forum_threads t ON t.id=r.thread_id WHERE t.channel_id=$1 AND r.id>$2 AND (r.decision<>'')=$3 ORDER BY r.id LIMIT $4`, req.ChannelId, req.AfterId, req.Resolved, limit+1)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	out := new(pb.ListForumReportsResponse)
	for rows.Next() {
		r, e := scanForumReport(rows)
		if e != nil {
			return nil, databaseError(ctx, e)
		}
		if len(out.Reports) == limit {
			out.NextId = out.Reports[limit-1].Id
			break
		}
		out.Reports = append(out.Reports, r)
	}
	return out, databaseErrorOrNil(ctx, rows.Err())
}

func hideReportedContent(ctx context.Context, tx pgx.Tx, t *pb.ForumThread, r *pb.ForumReport, revision int64, reason string) error {
	current, hidden, err := reportTarget(ctx, tx, t, r.ReplyId)
	if err != nil {
		return err
	}
	if current != revision {
		return status.Error(codes.Aborted, "Содержимое изменилось")
	}
	if hidden {
		return status.Error(codes.FailedPrecondition, "Содержимое уже скрыто")
	}
	action := "hide"
	if r.ReplyId != "" {
		action = "hide_reply"
		_, err = tx.Exec(ctx, "UPDATE forum_replies SET hidden=true,revision=revision+1 WHERE id=$1 AND thread_id=$2", r.ReplyId, t.Id)
	} else {
		_, err = tx.Exec(ctx, "UPDATE forum_threads SET hidden=true WHERE id=$1", t.Id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, "UPDATE forum_threads SET revision=revision+1 WHERE id=$1", t.Id)
	}
	if err != nil {
		return databaseError(ctx, err)
	}
	return recordForumModeration(ctx, tx, t, r.ReplyId, action, reason, current+1)
}

func (s *Store) ResolveForumReport(ctx context.Context, req *pb.ResolveForumReportRequest) (*pb.ResolveForumReportResponse, error) {
	if err := moderationReason(req.Reason); err != nil {
		return nil, err
	}
	if req.Decision != "dismiss" && req.Decision != "hide" {
		return nil, status.Error(codes.InvalidArgument, "Неизвестное решение")
	}
	tx, err := s.forumTransaction(ctx, req.ChannelId, "manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	r, err := scanForumReport(tx.QueryRow(ctx, `SELECT `+reportColumns+` FROM forum_reports r JOIN forum_threads t ON t.id=r.thread_id WHERE t.channel_id=$1 AND r.id=$2`, req.ChannelId, req.ReportId))
	if err != nil {
		return nil, forumResultError(ctx, err)
	}
	if r.Decision != "" {
		return nil, status.Error(codes.Aborted, "Жалоба уже рассмотрена")
	}
	t, err := forumThread(ctx, tx, req.ChannelId, r.ThreadId)
	if err != nil {
		return nil, err
	}
	if req.Decision == "hide" {
		err = hideReportedContent(ctx, tx, t, r, req.ExpectedRevision, req.Reason)
	}
	if err != nil {
		return nil, err
	}
	r, err = scanForumReport(tx.QueryRow(ctx, `UPDATE forum_reports r SET decision=$2,decision_reason=$3,moderator_id=$4,resolved_at_unix=extract(epoch FROM now())::bigint WHERE id=$1 RETURNING `+reportColumns, r.Id, req.Decision, strings.TrimSpace(req.Reason), authn.Actor(ctx)))
	if err == nil {
		err = recordReportDecision(ctx, tx, t, r)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return &pb.ResolveForumReportResponse{Report: r}, databaseErrorOrNil(ctx, err)
}

func recordReportDecision(ctx context.Context, tx pgx.Tx, t *pb.ForumThread, r *pb.ForumReport) error {
	var revision int64
	err := tx.QueryRow(ctx, "SELECT revision FROM forum_threads WHERE id=$1", t.Id).Scan(&revision)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO forum_moderation(thread_id,reply_id,action,reason,actor_id,revision,report_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, t.Id, r.ReplyId, "report_"+r.Decision, r.DecisionReason, authn.Actor(ctx), revision, r.Id)
	}
	if err != nil {
		return databaseError(ctx, err)
	}
	return audit(ctx, tx, authn.Actor(ctx), "forum.report_"+r.Decision+":"+t.ChannelId+":"+t.Id+":"+fmt.Sprint(r.Id), revision)
}
