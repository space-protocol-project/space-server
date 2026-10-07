package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func forbidden() error {
	return status.Error(codes.PermissionDenied, "Нет доступа к пространству или действию")
}

func membership(ctx context.Context, q rowQuery, principal string, lock bool) (*pb.Member, error) {
	var owner *string
	if err := q.QueryRow(ctx, "SELECT owner_id FROM space_settings WHERE singleton=true").Scan(&owner); err != nil {
		return nil, databaseError(ctx, err)
	}
	if owner != nil && *owner == principal {
		return &pb.Member{PrincipalId: principal, Role: "owner", Revision: 1}, nil
	}
	sql := "SELECT principal_id,role,blocked,revision FROM memberships WHERE principal_id=$1"
	if lock {
		sql += " FOR SHARE"
	}
	m := new(pb.Member)
	err := q.QueryRow(ctx, sql, principal).Scan(&m.PrincipalId, &m.Role, &m.Blocked, &m.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, forbidden()
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	return m, nil
}

func chatAccess(ctx context.Context, q rowQuery, write, lock bool) error {
	if authn.Token(ctx) == "" {
		return nil
	} // Только внутренние вызовы, не сетевой API.
	m, err := membership(ctx, q, authn.Actor(ctx), lock)
	if err != nil {
		return err
	}
	if m.Blocked || (write && m.Role == "reader") {
		return forbidden()
	}
	return nil
}

func (s *Store) GetMembership(ctx context.Context, _ *pb.GetMembershipRequest) (*pb.GetMembershipResponse, error) {
	m, err := membership(ctx, s.pool, authn.Actor(ctx), false)
	if err != nil {
		return nil, err
	}
	return &pb.GetMembershipResponse{Member: m}, nil
}

func managementRole(ctx context.Context, tx pgx.Tx, ownerOnly bool) (string, error) {
	principal, err := authorizeManagement(ctx, tx)
	if err != nil {
		return "", err
	}
	m, err := membership(ctx, tx, principal, true)
	if err != nil {
		return "", err
	}
	if m.Blocked || (m.Role != "owner" && (ownerOnly || m.Role != "admin")) {
		return "", forbidden()
	}
	return principal, nil
}

func audit(ctx context.Context, tx pgx.Tx, principal, action string, revision int64) error {
	_, err := tx.Exec(ctx, "INSERT INTO admin_audit(principal_id,action,revision) VALUES($1,$2,$3)", principal, action, revision)
	return err
}

func (s *Store) ListMembers(ctx context.Context, req *pb.ListMembersRequest) (*pb.ListMembersResponse, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if _, err = managementRole(ctx, tx, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT p.id,CASE WHEN s.owner_id=p.id THEN 'owner' ELSE m.role END,
 CASE WHEN s.owner_id=p.id THEN false ELSE m.blocked END,m.revision
 FROM memberships m JOIN principals p ON p.id=m.principal_id CROSS JOIN space_settings s
 WHERE p.id>$1 ORDER BY p.id LIMIT 101`, req.After)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := new(pb.ListMembersResponse)
	for rows.Next() {
		m := new(pb.Member)
		if err = rows.Scan(&m.PrincipalId, &m.Role, &m.Blocked, &m.Revision); err != nil {
			return nil, databaseError(ctx, err)
		}
		result.Members = append(result.Members, m)
	}
	if err = rows.Err(); err != nil {
		return nil, databaseError(ctx, err)
	}
	if len(result.Members) > 100 {
		result.Members = result.Members[:100]
		result.NextCursor = result.Members[99].PrincipalId
	}
	return result, nil
}

func (s *Store) UpdateMember(ctx context.Context, req *pb.UpdateMemberRequest) (*pb.UpdateMemberResponse, error) {
	if req.Role != "reader" && req.Role != "member" && req.Role != "admin" {
		return nil, status.Error(codes.InvalidArgument, "Роль: reader, member или admin")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	principal, err := managementRole(ctx, tx, true)
	if err != nil {
		return nil, err
	}
	var owner *string
	if err = tx.QueryRow(ctx, "SELECT owner_id FROM space_settings WHERE singleton=true").Scan(&owner); err != nil {
		return nil, databaseError(ctx, err)
	}
	if owner != nil && *owner == req.PrincipalId {
		return nil, forbidden()
	}
	m := &pb.Member{PrincipalId: req.PrincipalId}
	err = tx.QueryRow(ctx, "SELECT role,blocked,revision FROM memberships WHERE principal_id=$1 FOR UPDATE", req.PrincipalId).Scan(&m.Role, &m.Blocked, &m.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "Участник не найден")
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if m.Revision != req.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "Права уже изменились; загрузите список заново")
	}
	m.Role = req.Role
	m.Blocked = req.Blocked
	m.Revision++
	if _, err = tx.Exec(ctx, "UPDATE memberships SET role=$2,blocked=$3,revision=$4 WHERE principal_id=$1", m.PrincipalId, m.Role, m.Blocked, m.Revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = audit(ctx, tx, principal, "member.update:"+m.PrincipalId+":"+m.Role+":blocked="+strconv.FormatBool(m.Blocked), m.Revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.UpdateMemberResponse{Member: m}, nil
}

func validInviteRole(role string) bool { return role == "reader" || role == "member" }
func inviteHash(token string) ([]byte, error) {
	if len(token) != 46 || token[:3] != "iv_" {
		return nil, status.Error(codes.PermissionDenied, "Приглашение недействительно")
	}
	hash := sha256.Sum256([]byte(token))
	return hash[:], nil
}

func (s *Store) CreateInvite(ctx context.Context, req *pb.CreateInviteRequest) (*pb.CreateInviteResponse, error) {
	if !validInviteRole(req.Role) || req.TtlSeconds < 60 || req.TtlSeconds > 604800 || req.MaxUses < 1 || req.MaxUses > 100 {
		return nil, status.Error(codes.InvalidArgument, "Роль reader/member, срок 60–604800 секунд, использований 1–100")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	principal, err := managementRole(ctx, tx, false)
	if err != nil {
		return nil, err
	}
	// Общая блокировка ограничивает конкурентное создание и рост активного списка.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616003)"); err != nil {
		return nil, databaseError(ctx, err)
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM invitations WHERE NOT revoked AND expires_at>now() AND uses<max_uses").Scan(&count); err != nil {
		return nil, databaseError(ctx, err)
	}
	if count >= 100 {
		return nil, status.Error(codes.ResourceExhausted, "Лимит: 100 активных приглашений")
	}
	token, err := randomString("iv_")
	if err != nil {
		return nil, err
	}
	hash, _ := inviteHash(token)
	id, err := randomString("in_")
	if err != nil {
		return nil, err
	}
	invite := &pb.Invite{Id: id, Role: req.Role, ExpiresAt: time.Now().Add(time.Duration(req.TtlSeconds) * time.Second).Unix(), MaxUses: req.MaxUses}
	if _, err = tx.Exec(ctx, "INSERT INTO invitations(id,token_hash,role,expires_at,max_uses,created_by) VALUES($1,$2,$3,$4,$5,$6)", id, hash, req.Role, time.Unix(invite.ExpiresAt, 0), req.MaxUses, principal); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = audit(ctx, tx, principal, "invite.create:"+id, 0); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CreateInviteResponse{Invite: invite, Token: token}, nil
}

func (s *Store) ListInvites(ctx context.Context, req *pb.ListInvitesRequest) (*pb.ListInvitesResponse, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if _, err = managementRole(ctx, tx, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, "SELECT id,role,expires_at,max_uses,uses,revoked FROM invitations WHERE id>$1 ORDER BY id LIMIT 101", req.After)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := new(pb.ListInvitesResponse)
	for rows.Next() {
		v := new(pb.Invite)
		var expiry time.Time
		if err = rows.Scan(&v.Id, &v.Role, &expiry, &v.MaxUses, &v.Uses, &v.Revoked); err != nil {
			return nil, databaseError(ctx, err)
		}
		v.ExpiresAt = expiry.Unix()
		result.Invites = append(result.Invites, v)
	}
	if err = rows.Err(); err != nil {
		return nil, databaseError(ctx, err)
	}
	if len(result.Invites) > 100 {
		result.Invites = result.Invites[:100]
		result.NextCursor = result.Invites[99].Id
	}
	return result, nil
}

func (s *Store) RevokeInvite(ctx context.Context, req *pb.RevokeInviteRequest) (*pb.RevokeInviteResponse, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	principal, err := managementRole(ctx, tx, false)
	if err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, "UPDATE invitations SET revoked=true WHERE id=$1", req.InviteId)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if tag.RowsAffected() != 1 {
		return nil, status.Error(codes.NotFound, "Приглашение не найдено")
	}
	if err = audit(ctx, tx, principal, "invite.revoke:"+req.InviteId, 0); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.RevokeInviteResponse{}, nil
}

func availableInvite(ctx context.Context, q rowQuery, token string, lock bool) (*pb.Invite, error) {
	hash, err := inviteHash(token)
	if err != nil {
		return nil, err
	}
	sql := "SELECT id,role,expires_at,max_uses,uses,revoked FROM invitations WHERE token_hash=$1"
	if lock {
		sql += " FOR UPDATE"
	}
	v := new(pb.Invite)
	var expiry time.Time
	err = q.QueryRow(ctx, sql, hash).Scan(&v.Id, &v.Role, &expiry, &v.MaxUses, &v.Uses, &v.Revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, forbidden()
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	v.ExpiresAt = expiry.Unix()
	if v.Revoked || !expiry.After(time.Now()) {
		return nil, forbidden()
	}
	return v, nil
}

func (s *Store) PreviewInvite(ctx context.Context, req *pb.PreviewInviteRequest) (*pb.PreviewInviteResponse, error) {
	v, err := availableInvite(ctx, s.pool, req.Token, false)
	if err != nil {
		return nil, err
	}
	if v.Uses >= v.MaxUses {
		return nil, forbidden()
	}
	result := &pb.PreviewInviteResponse{Role: v.Role, ExpiresAt: v.ExpiresAt}
	err = s.pool.QueryRow(ctx, "SELECT i.server_id,s.title FROM server_state i CROSS JOIN space_settings s").Scan(&result.ServerId, &result.Title)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	return result, nil
}

func redeemInvite(ctx context.Context, tx pgx.Tx, token, principal string) (*pb.Member, error) {
	v, err := availableInvite(ctx, tx, token, true)
	if err != nil {
		return nil, err
	}
	// Сначала блокируем членство, чтобы приглашение не отменяло блокировку владельца.
	m, err := membership(ctx, tx, principal, true)
	if err != nil && status.Code(err) != codes.PermissionDenied {
		return nil, err
	}
	if m != nil && (m.Blocked || m.Role == "owner") {
		return nil, forbidden()
	}
	var used bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM invitation_redemptions WHERE invite_id=$1 AND principal_id=$2)", v.Id, principal).Scan(&used); err != nil {
		return nil, databaseError(ctx, err)
	}
	if used {
		return m, nil
	}
	if v.Uses >= v.MaxUses {
		return nil, forbidden()
	}
	// Приглашение не повышает и не понижает роль существующего участника.
	if m == nil {
		if _, err = tx.Exec(ctx, "INSERT INTO memberships(principal_id,role) VALUES($1,$2)", principal, v.Role); err != nil {
			return nil, databaseError(ctx, err)
		}
		m = &pb.Member{PrincipalId: principal, Role: v.Role, Revision: 1}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO invitation_redemptions(invite_id,principal_id) VALUES($1,$2)", v.Id, principal); err != nil {
		return nil, databaseError(ctx, err)
	}
	if _, err = tx.Exec(ctx, "UPDATE invitations SET uses=uses+1 WHERE id=$1", v.Id); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = audit(ctx, tx, principal, "invite.accept:"+v.Id, m.Revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	return m, nil
}

func (s *Store) AcceptInvite(ctx context.Context, req *pb.AcceptInviteRequest) (*pb.AcceptInviteResponse, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err = s.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	// Общая блокировка principal задаёт один порядок параллельных вступлений одного root.
	if _, err = tx.Exec(ctx, "SELECT id FROM principals WHERE id=$1 FOR UPDATE", authn.Actor(ctx)); err != nil {
		return nil, databaseError(ctx, err)
	}
	m, err := redeemInvite(ctx, tx, req.Token, authn.Actor(ctx))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.AcceptInviteResponse{Member: m}, nil
}
