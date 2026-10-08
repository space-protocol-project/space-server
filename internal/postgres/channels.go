package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var channelIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

const channelColumns = "id,title,view_type,position,archived,public_preview,revision"

func scanChannel(row pgx.Row) (*pb.Channel, error) {
	c := new(pb.Channel)
	var kind string
	err := row.Scan(&c.Id, &c.Title, &kind, &c.Position, &c.Archived, &c.PublicPreview, &c.Revision)
	c.Views = []*pb.View{{Id: kind, Type: kind}}
	return c, err
}

func loadChannel(ctx context.Context, q rowQuery, id, lock string) (*pb.Channel, error) {
	c, err := scanChannel(q.QueryRow(ctx, "SELECT "+channelColumns+" FROM channels WHERE id=$1 "+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "Канал не найден")
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	return c, nil
}

func deviceScopes(ctx context.Context, q rowQuery) ([]string, error) {
	hash := sha256.Sum256([]byte(authn.Token(ctx)))
	var scopes []string
	err := q.QueryRow(ctx, `SELECT g.scopes FROM auth_sessions s JOIN device_grants g ON g.id=s.grant_id JOIN principals p ON p.id=g.principal_id
 WHERE s.token_hash=$1 AND s.expires_at>now() AND g.expires_at>now() AND g.revoked_at IS NULL AND g.auth_epoch=p.auth_epoch`+activeParentCondition+activePairCondition, hash[:]).Scan(&scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, denied()
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	return scopes, nil
}

func hasScope(scopes []string, scope string) bool {
	for _, value := range scopes {
		if value == scope {
			return true
		}
	}
	return false
}

func effectivePermissions(ctx context.Context, q rowQuery, id string, m *pb.Member) (*pb.ChannelPermissions, error) {
	p := new(pb.ChannelPermissions)
	if m.Blocked {
		return p, nil
	}
	if m.Role == "owner" || m.Role == "admin" {
		p = &pb.ChannelPermissions{Visible: true, Read: true, Write: true, Manage: true}
	} else {
		err := q.QueryRow(ctx, `SELECT visible,can_read,can_write,can_manage FROM channel_access
 WHERE channel_id=$1 AND (principal_id=$2 OR role=$3) ORDER BY principal_id IS NOT NULL DESC LIMIT 1`, id, m.PrincipalId, m.Role).Scan(&p.Visible, &p.Read, &p.Write, &p.Manage)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, databaseError(ctx, err)
		}
	}
	// ACL не может отменить глобальный запрет на запись для reader.
	p.Write = p.Write && m.Role != "reader"
	scopes, err := deviceScopes(ctx, q)
	if err != nil {
		return nil, err
	}
	p.Read = p.Read && hasScope(scopes, "chat.read")
	p.Write = p.Write && p.Read && hasScope(scopes, "chat.write")
	p.Manage = p.Manage && hasScope(scopes, "space.manage")
	return p, nil
}

// В транзакции порядок блокировок: grant → membership → settings → channel.
// ACL изменяется только под FOR UPDATE канала; чтение удерживает FOR SHARE.
func channelAccess(ctx context.Context, q rowQuery, id, action string, lock bool) (*pb.Channel, error) {
	var m *pb.Member
	if authn.Token(ctx) != "" {
		var err error
		m, err = membership(ctx, q, authn.Actor(ctx), lock)
		if err != nil {
			return nil, err
		}
		if m.Blocked {
			return nil, forbidden()
		}
	}
	var disabled bool
	if err := enabledChat(ctx, q, lock); err != nil {
		if action != "visible" || status.Code(err) != codes.NotFound {
			return nil, err
		}
		disabled = true
	}
	clause := ""
	if lock {
		clause = "FOR SHARE"
	}
	if lock && action == "write" {
		clause = "FOR UPDATE"
	}
	c, err := loadChannel(ctx, q, id, clause)
	if err != nil {
		return nil, err
	}
	if m == nil {
		// Только прямые внутренние вызовы без сетевого interceptor.
		c.Permissions = &pb.ChannelPermissions{Visible: true, Read: true, Write: true, Manage: true}
	} else {
		c.Permissions, err = effectivePermissions(ctx, q, id, m)
		if err != nil {
			return nil, err
		}
	}
	if !c.Permissions.Visible {
		return nil, status.Error(codes.NotFound, "Канал не найден")
	}
	allowed := c.Permissions.Visible
	switch action {
	case "read":
		allowed = c.Permissions.Read
	case "write":
		allowed = c.Permissions.Write
	case "manage":
		allowed = c.Permissions.Manage
	}
	if !allowed {
		return nil, forbidden()
	}
	if action == "write" && c.Archived {
		return nil, status.Error(codes.FailedPrecondition, "Канал архивирован")
	}
	if c.Archived {
		c.Permissions.Write = false
	}
	if disabled {
		c.Permissions.Read = false
		c.Permissions.Write = false
	}
	return c, nil
}

func validChannelMetadata(title string, position int32) bool {
	return strings.TrimSpace(title) != "" && len(title) <= 320 && position >= 0 && position <= 100000
}

func (s *Store) channelList(ctx context.Context, tx pgx.Tx, archived, publicOnly, includeDisabled bool) ([]*pb.Channel, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, "SELECT chat_enabled FROM space_settings WHERE singleton=true FOR SHARE").Scan(&enabled); err != nil {
		return nil, databaseError(ctx, err)
	}
	if !enabled && !includeDisabled {
		return nil, nil
	}
	// Сначала закрываем rows: пул ограничен, повторные запросы выполняются той же транзакцией.
	rows, err := tx.Query(ctx, "SELECT id FROM channels WHERE ($1 OR NOT archived) AND (NOT $2 OR public_preview) ORDER BY position,id", archived, publicOnly)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, databaseError(ctx, err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, databaseError(ctx, err)
	}
	var result []*pb.Channel
	for _, id := range ids {
		if publicOnly {
			c, err := loadChannel(ctx, tx, id, "FOR SHARE")
			if err != nil {
				return nil, err
			}
			// Повторная проверка после блокировки предотвращает утечку при смене public_preview.
			if c.PublicPreview && !c.Archived {
				result = append(result, c)
			}
			continue
		}
		c, err := channelAccess(ctx, tx, id, "visible", true)
		if status.Code(err) == codes.NotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		if archived || !c.Archived {
			result = append(result, c)
		}
	}
	return result, nil
}

func (s *Store) ListChannels(ctx context.Context, req *pb.ListChannelsRequest) (*pb.ListChannelsResponse, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err = s.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	m, err := membership(ctx, tx, authn.Actor(ctx), true)
	if err != nil {
		return nil, err
	}
	if m.Blocked {
		return nil, forbidden()
	}
	channels, err := s.channelList(ctx, tx, req.IncludeArchived, false, true)
	return &pb.ListChannelsResponse{Channels: channels}, err
}

func (s *Store) CreateChannel(ctx context.Context, req *pb.CreateChannelRequest) (*pb.CreateChannelResponse, error) {
	if !channelIDPattern.MatchString(req.ChannelId) || !validChannelMetadata(req.Title, req.Position) || req.ViewType != "chat" {
		return nil, status.Error(codes.InvalidArgument, "Нужны id [a-z][a-z0-9_-] до 64 символов, название до 320 байт, position 0–100000 и view_type chat")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	actor, err := managementRole(ctx, tx, false)
	if err != nil {
		return nil, err
	}
	// Отдельная блокировка для лимита каналов, без блокировки registry identity.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616004)"); err != nil {
		return nil, databaseError(ctx, err)
	}
	var exists bool
	var count int
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM channels WHERE id=$1),count(*) FROM channels", req.ChannelId).Scan(&exists, &count); err != nil {
		return nil, databaseError(ctx, err)
	}
	if exists {
		return nil, status.Error(codes.AlreadyExists, "Канал с таким id уже существует")
	}
	if count >= 100 {
		return nil, status.Error(codes.ResourceExhausted, "Лимит прототипа: 100 каналов, включая архив")
	}
	if _, err = tx.Exec(ctx, "INSERT INTO channels(id,title,view_type,position,public_preview) VALUES($1,$2,$3,$4,$5)", req.ChannelId, req.Title, req.ViewType, req.Position, req.PublicPreview); err != nil {
		return nil, databaseError(ctx, err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO channel_access(channel_id,subject,role,visible,can_read,can_write,can_manage) VALUES
 ($1,'role:member','member',true,true,true,false),($1,'role:reader','reader',true,true,false,false)`, req.ChannelId); err != nil {
		return nil, databaseError(ctx, err)
	}
	c, err := loadChannel(ctx, tx, req.ChannelId, "")
	if err != nil {
		return nil, err
	}
	m, err := membership(ctx, tx, actor, false)
	if err != nil {
		return nil, err
	}
	c.Permissions, err = effectivePermissions(ctx, tx, c.Id, m)
	if err != nil {
		return nil, err
	}
	if err = audit(ctx, tx, actor, "channel.create:"+c.Id, c.Revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CreateChannelResponse{Channel: c}, nil
}

func (s *Store) UpdateChannel(ctx context.Context, req *pb.UpdateChannelRequest) (*pb.UpdateChannelResponse, error) {
	if !validChannelMetadata(req.Title, req.Position) || req.ExpectedRevision < 1 {
		return nil, status.Error(codes.InvalidArgument, "Некорректные название, position или expected_revision")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	// Включая делегированного управляющего: scope устройства обязателен.
	actor, err := authorizeManagement(ctx, tx)
	if err != nil {
		return nil, err
	}
	m, err := membership(ctx, tx, actor, true)
	if err != nil {
		return nil, err
	}
	if m.Blocked {
		return nil, forbidden()
	}
	// settings раньше channel: совместимость старого API chat_title.
	if _, err = tx.Exec(ctx, "SELECT singleton FROM space_settings FOR UPDATE"); err != nil {
		return nil, databaseError(ctx, err)
	}
	c, err := loadChannel(ctx, tx, req.ChannelId, "FOR UPDATE")
	if err != nil {
		return nil, err
	}
	p, err := effectivePermissions(ctx, tx, c.Id, m)
	if err != nil {
		return nil, err
	}
	if !p.Visible {
		return nil, status.Error(codes.NotFound, "Канал не найден")
	}
	if !p.Manage {
		return nil, forbidden()
	}
	if req.PublicPreview != c.PublicPreview && m.Role != "owner" && m.Role != "admin" {
		return nil, forbidden()
	}
	if req.ExpectedRevision != c.Revision {
		return nil, status.Error(codes.Aborted, "Канал уже изменился; загрузите его заново")
	}
	c.Title, c.Position, c.Archived, c.PublicPreview = req.Title, req.Position, req.Archived, req.PublicPreview
	c.Revision++
	c.Permissions = p
	if c.Archived {
		c.Permissions.Write = false
	}
	if _, err = tx.Exec(ctx, "UPDATE channels SET title=$2,position=$3,archived=$4,public_preview=$5,revision=$6 WHERE id=$1", c.Id, c.Title, c.Position, c.Archived, c.PublicPreview, c.Revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	if c.Id == "general" {
		if _, err = tx.Exec(ctx, "UPDATE space_settings SET chat_title=$1,revision=revision+1 WHERE singleton=true AND chat_title<>$1", c.Title); err != nil {
			return nil, databaseError(ctx, err)
		}
	}
	if err = audit(ctx, tx, actor, "channel.update:"+c.Id, c.Revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.UpdateChannelResponse{Channel: c}, nil
}

func validPermissions(p *pb.ChannelPermissions) bool {
	return p != nil && (!p.Write || p.Read) && (!p.Read || p.Visible) && (!p.Manage || p.Visible)
}

func validateRules(rules []*pb.ChannelAccessRule) error {
	if len(rules) > 100 {
		return status.Error(codes.InvalidArgument, "Лимит: 100 правил на канал")
	}
	seen := make(map[string]bool)
	for _, r := range rules {
		if r == nil || !validPermissions(r.Permissions) {
			return status.Error(codes.InvalidArgument, "Некорректная иерархия разрешений")
		}
		subject := "role:" + r.Role
		if r.PrincipalId != "" {
			subject = "principal:" + r.PrincipalId
		}
		if (r.Role == "" && r.PrincipalId == "") || (r.Role != "" && r.PrincipalId != "") || (r.Role != "" && r.Role != "member" && r.Role != "reader") || len(r.PrincipalId) > 128 || seen[subject] {
			return status.Error(codes.InvalidArgument, "Нужен уникальный субъект role member/reader либо principal_id")
		}
		seen[subject] = true
	}
	return nil
}

func readRules(ctx context.Context, tx pgx.Tx, c *pb.Channel) (*pb.GetChannelAccessResponse, error) {
	rows, err := tx.Query(ctx, "SELECT COALESCE(role,''),COALESCE(principal_id,''),visible,can_read,can_write,can_manage FROM channel_access WHERE channel_id=$1 ORDER BY subject", c.Id)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := &pb.GetChannelAccessResponse{ChannelId: c.Id, Revision: c.Revision}
	for rows.Next() {
		r := &pb.ChannelAccessRule{Permissions: new(pb.ChannelPermissions)}
		if err = rows.Scan(&r.Role, &r.PrincipalId, &r.Permissions.Visible, &r.Permissions.Read, &r.Permissions.Write, &r.Permissions.Manage); err != nil {
			return nil, databaseError(ctx, err)
		}
		result.Rules = append(result.Rules, r)
	}
	if err = rows.Err(); err != nil {
		return nil, databaseError(ctx, err)
	}
	return result, nil
}

func (s *Store) GetChannelAccess(ctx context.Context, req *pb.GetChannelAccessRequest) (*pb.GetChannelAccessResponse, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if _, err = managementRole(ctx, tx, false); err != nil {
		return nil, err
	}
	c, err := loadChannel(ctx, tx, req.ChannelId, "FOR SHARE")
	if err != nil {
		return nil, err
	}
	return readRules(ctx, tx, c)
}

func (s *Store) UpdateChannelAccess(ctx context.Context, req *pb.UpdateChannelAccessRequest) (*pb.UpdateChannelAccessResponse, error) {
	if err := validateRules(req.Rules); err != nil {
		return nil, err
	}
	if req.ExpectedRevision < 1 {
		return nil, status.Error(codes.InvalidArgument, "Нужен expected_revision")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	actor, err := managementRole(ctx, tx, false)
	if err != nil {
		return nil, err
	}
	c, err := loadChannel(ctx, tx, req.ChannelId, "FOR UPDATE")
	if err != nil {
		return nil, err
	}
	if c.Revision != req.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "Канал уже изменился; загрузите права заново")
	}
	// Проверяем всех субъектов до удаления старых правил. Блокировка пространства выше ACL.
	for _, r := range req.Rules {
		if r.PrincipalId == "" {
			continue
		}
		m, err := membership(ctx, tx, r.PrincipalId, false)
		if err != nil {
			if status.Code(err) == codes.PermissionDenied {
				return nil, status.Error(codes.InvalidArgument, "Субъект не является участником пространства")
			}
			return nil, err
		}
		if m.Role == "owner" || m.Role == "admin" {
			return nil, status.Error(codes.InvalidArgument, "Правила для owner/admin не применяются")
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM channel_access WHERE channel_id=$1", c.Id); err != nil {
		return nil, databaseError(ctx, err)
	}
	for _, r := range req.Rules {
		subject := "role:" + r.Role
		var role, principal any
		if r.Role != "" {
			role = r.Role
		} else {
			principal = r.PrincipalId
			subject = "principal:" + r.PrincipalId
		}
		p := r.Permissions
		if _, err = tx.Exec(ctx, "INSERT INTO channel_access(channel_id,subject,role,principal_id,visible,can_read,can_write,can_manage) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", c.Id, subject, role, principal, p.Visible, p.Read, p.Write, p.Manage); err != nil {
			return nil, databaseError(ctx, err)
		}
	}
	c.Revision++
	if _, err = tx.Exec(ctx, "UPDATE channels SET revision=$2 WHERE id=$1", c.Id, c.Revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = audit(ctx, tx, actor, "channel.access.update:"+c.Id, c.Revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	result, err := readRules(ctx, tx, c)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.UpdateChannelAccessResponse{ChannelId: result.ChannelId, Revision: result.Revision, Rules: result.Rules}, nil
}
