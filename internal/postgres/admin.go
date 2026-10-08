package postgres

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"time"
)

type rowQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func enabledChat(ctx context.Context, query rowQuery, lock bool) error {
	sql := "SELECT chat_enabled FROM space_settings WHERE singleton=true"
	if lock {
		sql += " FOR SHARE"
	}
	var enabled bool
	if err := query.QueryRow(ctx, sql).Scan(&enabled); err != nil {
		return databaseError(ctx, err)
	}
	if !enabled {
		return status.Error(codes.NotFound, "Раздел недоступен")
	}
	return nil
}
func (s *Store) GetManifest(ctx context.Context, _ *pb.GetManifestRequest) (*pb.GetManifestResponse, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	publicOnly := authn.Token(ctx) == ""
	if !publicOnly {
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
	}
	var title, serverID string
	err = tx.QueryRow(ctx, "SELECT s.title,i.server_id FROM space_settings s CROSS JOIN server_state i WHERE s.singleton=true AND i.singleton=true").Scan(&title, &serverID)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	response := &pb.GetManifestResponse{ProtocolVersion: "0.1-experimental", ServerId: serverID, Title: title}
	response.Channels, err = s.channelList(ctx, tx, false, publicOnly)
	return response, err
}
func (s *Store) CreateSetupCode(ctx context.Context) (string, error) {
	code, err := randomString("setup_")
	if err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var initialized bool
	if err := tx.QueryRow(ctx, "SELECT owner_id IS NOT NULL FROM space_settings WHERE singleton=true FOR UPDATE").Scan(&initialized); err != nil {
		return "", err
	}
	if initialized {
		return "", errors.New("Владелец уже назначен; первичная настройка закрыта")
	}
	hash := sha256.Sum256([]byte(code))
	if _, err = tx.Exec(ctx, "UPDATE space_settings SET setup_code_hash=$1,setup_expires_at=now()+interval '15 minutes' WHERE singleton=true", hash[:]); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return code, nil
}
func (s *Store) GetSetupStatus(ctx context.Context, _ *pb.GetSetupStatusRequest) (*pb.GetSetupStatusResponse, error) {
	var initialized bool
	if err := s.pool.QueryRow(ctx, "SELECT owner_id IS NOT NULL FROM space_settings WHERE singleton=true").Scan(&initialized); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.GetSetupStatusResponse{Initialized: initialized}, nil
}
func authorizeManagement(ctx context.Context, tx pgx.Tx) (string, error) {
	token := authn.Token(ctx)
	if token == "" {
		return "", denied()
	}
	hash := sha256.Sum256([]byte(token))
	var principal string
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT p.id,'space.manage'=ANY(g.scopes) FROM auth_sessions s JOIN device_grants g ON g.id=s.grant_id JOIN principals p ON p.id=g.principal_id WHERE s.token_hash=$1 AND s.expires_at>now() AND g.expires_at>now() AND g.revoked_at IS NULL AND g.auth_epoch=p.auth_epoch`+activeParentCondition+activePairCondition+` FOR SHARE OF g`, hash[:]).Scan(&principal, &allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", denied()
	}
	if err != nil {
		return "", databaseError(ctx, err)
	}
	if !allowed {
		return "", status.Error(codes.PermissionDenied, "Устройство не имеет разрешения space.manage")
	}
	return principal, nil
}
func settings(ctx context.Context, tx pgx.Tx, lock string) (*pb.SpaceSettings, string, []byte, *time.Time, error) {
	result := new(pb.SpaceSettings)
	var owner *string
	var hash []byte
	var expires *time.Time
	err := tx.QueryRow(ctx, "SELECT title,chat_title,chat_enabled,registration_policy,revision,owner_id,setup_code_hash,setup_expires_at FROM space_settings WHERE singleton=true "+lock).Scan(&result.Title, &result.ChatTitle, &result.ChatEnabled, &result.RegistrationPolicy, &result.Revision, &owner, &hash, &expires)
	id := ""
	if owner != nil {
		id = *owner
	}
	return result, id, hash, expires, err
}
func (s *Store) ClaimOwner(ctx context.Context, req *pb.ClaimOwnerRequest) (*pb.ClaimOwnerResponse, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	principal, err := authorizeManagement(ctx, tx)
	if err != nil {
		return nil, err
	}
	config, owner, hash, expires, err := settings(ctx, tx, "FOR UPDATE")
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	candidate := sha256.Sum256([]byte(req.SetupCode))
	if owner != "" || expires == nil || time.Now().After(*expires) || len(hash) != 32 || subtle.ConstantTimeCompare(hash, candidate[:]) != 1 {
		return nil, status.Error(codes.PermissionDenied, "Код недействителен или первичная настройка закрыта")
	}
	if _, err = tx.Exec(ctx, "UPDATE space_settings SET owner_id=$1,setup_code_hash=NULL,setup_expires_at=NULL,revision=revision+1 WHERE singleton=true", principal); err != nil {
		return nil, databaseError(ctx, err)
	}
	config.Revision++
	if _, err = tx.Exec(ctx, "INSERT INTO admin_audit(principal_id,action,revision) VALUES($1,'owner.claim',$2)", principal, config.Revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.ClaimOwnerResponse{Settings: config}, nil
}
func (s *Store) GetSettings(ctx context.Context, _ *pb.GetSettingsRequest) (*pb.GetSettingsResponse, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	principal, err := managementRole(ctx, tx, false)
	if err != nil {
		return nil, err
	}
	config, owner, _, _, err := settings(ctx, tx, "FOR SHARE")
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if owner != principal {
		member, roleErr := membership(ctx, tx, principal, false)
		if roleErr != nil || member.Role != "admin" {
			return nil, forbidden()
		}
	}
	return &pb.GetSettingsResponse{Settings: config}, nil
}
func (s *Store) UpdateSettings(ctx context.Context, req *pb.UpdateSettingsRequest) (*pb.UpdateSettingsResponse, error) {
	if strings.TrimSpace(req.Title) == "" || len(req.Title) > 320 || strings.TrimSpace(req.ChatTitle) == "" || len(req.ChatTitle) > 320 || (req.RegistrationPolicy != "open" && req.RegistrationPolicy != "closed") {
		return nil, status.Error(codes.InvalidArgument, "Нужны названия до 320 байт и политика open/closed")
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
	current, owner, _, _, err := settings(ctx, tx, "FOR UPDATE")
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if owner != principal {
		member, roleErr := membership(ctx, tx, principal, false)
		if roleErr != nil || member.Role != "admin" {
			return nil, forbidden()
		}
	}
	if current.Revision != req.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "Настройки уже изменились; загрузите их заново")
	}
	revision := current.Revision + 1
	if _, err = tx.Exec(ctx, "UPDATE space_settings SET title=$1,chat_title=$2,chat_enabled=$3,registration_policy=$4,revision=$5 WHERE singleton=true", req.Title, req.ChatTitle, req.ChatEnabled, req.RegistrationPolicy, revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	// Старое поле chat_title остаётся совместимым с названием general.
	if _, err = tx.Exec(ctx, "UPDATE channels SET title=$1,revision=revision+1 WHERE id='general' AND title<>$1", req.ChatTitle); err != nil {
		return nil, databaseError(ctx, err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO admin_audit(principal_id,action,revision) VALUES($1,'settings.update',$2)", principal, revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.UpdateSettingsResponse{Settings: &pb.SpaceSettings{Title: req.Title, ChatTitle: req.ChatTitle, ChatEnabled: req.ChatEnabled, RegistrationPolicy: req.RegistrationPolicy, Revision: revision}}, nil
}
