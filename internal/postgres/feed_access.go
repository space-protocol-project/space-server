package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func feedPermissions(ctx context.Context, tx pgx.Tx, channel string) (*pb.FeedPermissions, error) {
	m, err := membership(ctx, tx, authn.Actor(ctx), false)
	if err != nil {
		return nil, err
	}
	p := new(pb.FeedPermissions)
	if m.Role == "owner" || m.Role == "admin" {
		p.Publish, p.Comment = true, true
	} else {
		err = tx.QueryRow(ctx, `SELECT can_publish,can_comment FROM feed_access WHERE channel_id=$1 AND (principal_id=$2 OR role=$3) ORDER BY principal_id IS NOT NULL DESC LIMIT 1`, channel, m.PrincipalId, m.Role).Scan(&p.Publish, &p.Comment)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, databaseError(ctx, err)
		}
	}
	scopes, err := deviceScopes(ctx, tx)
	if err != nil {
		return nil, err
	}
	c, err := loadChannel(ctx, tx, channel, "")
	if err != nil {
		return nil, err
	}
	enabled := !m.Blocked && !c.Archived && hasScope(scopes, "chat.write")
	p.Publish = p.Publish && enabled && m.Role != "reader"
	p.Comment = p.Comment && enabled
	return p, nil
}

func requireFeedAction(ctx context.Context, tx pgx.Tx, c *pb.Channel, action string) error {
	if c.Views[0].Type != "feed" {
		return status.Error(codes.FailedPrecondition, "Нужен раздел ленты")
	}
	if !c.Permissions.Read {
		return forbidden()
	}
	if action == "read" || action == "manage_read" {
		return nil
	}
	if c.Archived {
		return status.Error(codes.FailedPrecondition, "Раздел архивирован")
	}
	if action == "manage" || action == "report" {
		return nil
	}
	p, err := feedPermissions(ctx, tx, c.Id)
	if err != nil {
		return err
	}
	allowed := (action == "feed.publish" && p.Publish) || (action == "feed.comment" && p.Comment) || (action == "feed.upload" && (p.Publish || p.Comment))
	if !allowed {
		return forbidden()
	}
	return nil
}

func (s *Store) feedTransaction(ctx context.Context, channel, action string) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = s.lockSession(ctx, tx); err == nil {
		access := action
		if action == "manage_read" {
			access = "manage"
		}
		if action == "report" {
			access = "feed.report"
		}
		var c *pb.Channel
		c, err = channelAccess(ctx, tx, channel, access, true)
		if err == nil {
			err = requireFeedAction(ctx, tx, c, action)
		}
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func readFeedRules(ctx context.Context, tx pgx.Tx, channel string) (*pb.GetFeedAccessResponse, error) {
	c, err := loadChannel(ctx, tx, channel, "")
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT COALESCE(role,''),COALESCE(principal_id,''),can_publish,can_comment FROM feed_access WHERE channel_id=$1 ORDER BY subject`, channel)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	out := &pb.GetFeedAccessResponse{ChannelId: channel, Revision: c.Revision}
	for rows.Next() {
		r := &pb.FeedAccessRule{Permissions: new(pb.FeedPermissions)}
		if err = rows.Scan(&r.Role, &r.PrincipalId, &r.Permissions.Publish, &r.Permissions.Comment); err != nil {
			return nil, databaseError(ctx, err)
		}
		out.Rules = append(out.Rules, r)
	}
	return out, databaseErrorOrNil(ctx, rows.Err())
}

func (s *Store) GetFeedAccess(ctx context.Context, req *pb.GetFeedAccessRequest) (*pb.GetFeedAccessResponse, error) {
	tx, err := s.feedTransaction(ctx, req.ChannelId, "manage_read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	return readFeedRules(ctx, tx, req.ChannelId)
}

func validateFeedRules(ctx context.Context, tx pgx.Tx, rules []*pb.FeedAccessRule) error {
	if len(rules) > 200 {
		return status.Error(codes.InvalidArgument, "Не более 200 правил")
	}
	seen := map[string]bool{}
	subjects := make([]*pb.ChannelAccessRule, 0, len(rules))
	for _, r := range rules {
		if r == nil || r.Permissions == nil || (r.Role == "") == (r.PrincipalId == "") {
			return status.Error(codes.InvalidArgument, "Укажите ровно одного субъекта и права")
		}
		if r.Role != "" && r.Role != "member" && r.Role != "reader" {
			return status.Error(codes.InvalidArgument, "Неизвестная роль")
		}
		subject := r.Role + ":" + r.PrincipalId
		if seen[subject] {
			return status.Error(codes.InvalidArgument, "Субъект повторяется")
		}
		seen[subject] = true
		subjects = append(subjects, &pb.ChannelAccessRule{Role: r.Role, PrincipalId: r.PrincipalId})
	}
	return validateAccessMembers(ctx, tx, subjects)
}

func (s *Store) UpdateFeedAccess(ctx context.Context, req *pb.UpdateFeedAccessRequest) (*pb.UpdateFeedAccessResponse, error) {
	tx, err := s.feedTransaction(ctx, req.ChannelId, "manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	c, err := loadChannel(ctx, tx, req.ChannelId, "")
	if err != nil {
		return nil, err
	}
	if c.Revision != req.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "Раздел изменился")
	}
	if err = validateFeedRules(ctx, tx, req.Rules); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM feed_access WHERE channel_id=$1", req.ChannelId); err != nil {
		return nil, databaseError(ctx, err)
	}
	for _, r := range req.Rules {
		var role, principal any
		subject := "role:" + r.Role
		if r.Role != "" {
			role = r.Role
		} else {
			principal = r.PrincipalId
			subject = "principal:" + r.PrincipalId
		}
		_, err = tx.Exec(ctx, `INSERT INTO feed_access(channel_id,subject,role,principal_id,can_publish,can_comment) VALUES($1,$2,$3,$4,$5,$6)`, req.ChannelId, subject, role, principal, r.Permissions.Publish, r.Permissions.Comment)
		if err != nil {
			return nil, databaseError(ctx, err)
		}
	}
	c.Revision++
	if _, err = tx.Exec(ctx, "UPDATE channels SET revision=$2 WHERE id=$1", c.Id, c.Revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = audit(ctx, tx, authn.Actor(ctx), "feed.access.update:"+c.Id, c.Revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	out, err := readFeedRules(ctx, tx, c.Id)
	if err != nil {
		return nil, err
	}
	return &pb.UpdateFeedAccessResponse{ChannelId: out.ChannelId, Revision: out.Revision, Rules: out.Rules}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}
