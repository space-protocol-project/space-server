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

const feedReportColumns = "r.id,r.post_id,r.comment_id,r.reason,r.decision,r.decision_reason,r.moderator_id,r.created_at_unix,r.resolved_at_unix"

func scanFeedReport(row pgx.Row) (*pb.FeedReport, error) {
	r := new(pb.FeedReport)
	err := row.Scan(&r.Id, &r.PostId, &r.CommentId, &r.Reason, &r.Decision, &r.DecisionReason, &r.ModeratorId, &r.CreatedAtUnix, &r.ResolvedAtUnix)
	return r, err
}

func feedReportTarget(ctx context.Context, tx pgx.Tx, t *pb.FeedPost, comment string) (int64, bool, error) {
	if comment == "" {
		return t.Revision, t.Hidden, nil
	}
	var revision int64
	var hidden bool
	err := tx.QueryRow(ctx, "SELECT revision,hidden FROM feed_comments WHERE post_id=$1 AND id=$2", t.Id, comment).Scan(&revision, &hidden)
	return revision, hidden || t.Hidden, feedResultError(ctx, err)
}

func feedReportAllowance(ctx context.Context, tx pgx.Tx, post, comment string) error {
	var duplicate bool
	var recent int
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM feed_reports WHERE reporter_id=$1 AND post_id=$2 AND comment_id=$3),
 (SELECT count(*) FROM feed_reports WHERE reporter_id=$1 AND created_at_unix>extract(epoch FROM now())::bigint-3600)`, authn.Actor(ctx), post, comment).Scan(&duplicate, &recent)
	if err != nil {
		return databaseError(ctx, err)
	}
	if !duplicate && recent >= 30 {
		return status.Error(codes.ResourceExhausted, "Слишком много жалоб. Попробуйте позже")
	}
	return nil
}

func (s *Store) ReportFeedContent(ctx context.Context, req *pb.ReportFeedContentRequest) (*pb.ReportFeedContentResponse, error) {
	if err := moderationReason(req.Reason); err != nil {
		return nil, err
	}
	tx, err := s.feedTransaction(ctx, req.ChannelId, "report")
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
	t, err := feedPost(ctx, tx, req.ChannelId, req.PostId)
	if err != nil {
		return nil, err
	}
	_, hidden, err := feedReportTarget(ctx, tx, t, req.CommentId)
	if err != nil {
		return nil, err
	}
	if hidden {
		return nil, status.Error(codes.NotFound, "Содержимое недоступно")
	}
	// Блокировка участника сериализует лимит также между разными разделами.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "feed-report:"+authn.Actor(ctx)); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = feedReportAllowance(ctx, tx, t.Id, req.CommentId); err != nil {
		return nil, err
	}
	// Одна жалоба от участника на объект; повтор запроса не создаёт дубль.
	_, err = tx.Exec(ctx, `INSERT INTO feed_reports(post_id,comment_id,reporter_id,reason) VALUES($1,$2,$3,$4) ON CONFLICT(post_id,comment_id,reporter_id) DO NOTHING`, t.Id, req.CommentId, authn.Actor(ctx), strings.TrimSpace(req.Reason))
	if err == nil {
		err = tx.Commit(ctx)
	}
	return &pb.ReportFeedContentResponse{}, databaseErrorOrNil(ctx, err)
}

func (s *Store) ListFeedReports(ctx context.Context, req *pb.ListFeedReportsRequest) (*pb.ListFeedReportsResponse, error) {
	limit, err := forumPage(req.AfterId, req.Limit)
	if err != nil {
		return nil, err
	}
	tx, err := s.feedTransaction(ctx, req.ChannelId, "manage_read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+feedReportColumns+` FROM feed_reports r JOIN feed_posts t ON t.id=r.post_id WHERE t.channel_id=$1 AND r.id>$2 AND (r.decision<>'')=$3 ORDER BY r.id LIMIT $4`, req.ChannelId, req.AfterId, req.Resolved, limit+1)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	out := new(pb.ListFeedReportsResponse)
	for rows.Next() {
		r, e := scanFeedReport(rows)
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

func hideReportedFeedContent(ctx context.Context, tx pgx.Tx, t *pb.FeedPost, r *pb.FeedReport, revision int64, reason string) error {
	current, hidden, err := feedReportTarget(ctx, tx, t, r.CommentId)
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
	if r.CommentId != "" {
		action = "hide_comment"
		_, err = tx.Exec(ctx, "UPDATE feed_comments SET hidden=true,revision=revision+1 WHERE id=$1 AND post_id=$2", r.CommentId, t.Id)
	} else {
		_, err = tx.Exec(ctx, "UPDATE feed_posts SET hidden=true WHERE id=$1", t.Id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, "UPDATE feed_posts SET revision=revision+1 WHERE id=$1", t.Id)
	}
	if err != nil {
		return databaseError(ctx, err)
	}
	return recordFeedModeration(ctx, tx, t, r.CommentId, action, reason, current+1)
}

func (s *Store) ResolveFeedReport(ctx context.Context, req *pb.ResolveFeedReportRequest) (*pb.ResolveFeedReportResponse, error) {
	if err := moderationReason(req.Reason); err != nil {
		return nil, err
	}
	if req.Decision != "dismiss" && req.Decision != "hide" {
		return nil, status.Error(codes.InvalidArgument, "Неизвестное решение")
	}
	tx, err := s.feedTransaction(ctx, req.ChannelId, "manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	r, err := scanFeedReport(tx.QueryRow(ctx, `SELECT `+feedReportColumns+` FROM feed_reports r JOIN feed_posts t ON t.id=r.post_id WHERE t.channel_id=$1 AND r.id=$2`, req.ChannelId, req.ReportId))
	if err != nil {
		return nil, feedResultError(ctx, err)
	}
	if r.Decision != "" {
		return nil, status.Error(codes.Aborted, "Жалоба уже рассмотрена")
	}
	t, err := feedPost(ctx, tx, req.ChannelId, r.PostId)
	if err != nil {
		return nil, err
	}
	if req.Decision == "hide" {
		err = hideReportedFeedContent(ctx, tx, t, r, req.ExpectedRevision, req.Reason)
	}
	if err != nil {
		return nil, err
	}
	r, err = scanFeedReport(tx.QueryRow(ctx, `UPDATE feed_reports r SET decision=$2,decision_reason=$3,moderator_id=$4,resolved_at_unix=extract(epoch FROM now())::bigint WHERE id=$1 RETURNING `+feedReportColumns, r.Id, req.Decision, strings.TrimSpace(req.Reason), authn.Actor(ctx)))
	if err == nil {
		err = recordFeedReportDecision(ctx, tx, t, r)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return &pb.ResolveFeedReportResponse{Report: r}, databaseErrorOrNil(ctx, err)
}

func recordFeedReportDecision(ctx context.Context, tx pgx.Tx, t *pb.FeedPost, r *pb.FeedReport) error {
	var revision int64
	err := tx.QueryRow(ctx, "SELECT revision FROM feed_posts WHERE id=$1", t.Id).Scan(&revision)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO feed_moderation(post_id,comment_id,action,reason,actor_id,revision,report_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, t.Id, r.CommentId, "report_"+r.Decision, r.DecisionReason, authn.Actor(ctx), revision, r.Id)
	}
	if err != nil {
		return databaseError(ctx, err)
	}
	return audit(ctx, tx, authn.Actor(ctx), "feed.report_"+r.Decision+":"+t.ChannelId+":"+t.Id+":"+fmt.Sprint(r.Id), revision)
}
