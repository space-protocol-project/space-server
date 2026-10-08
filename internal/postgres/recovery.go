package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const recoveryLifetime = 3650 * 24 * time.Hour

func (s *AuthService) RevokeCurrentDevice(ctx context.Context, _ *pb.RevokeCurrentDeviceRequest) (*pb.RevokeCurrentDeviceResponse, error) {
	tx, err := s.store.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	hash := sha256.Sum256([]byte(authn.Token(ctx)))
	t := authn.Transcript{PrincipalID: authn.Actor(ctx)}
	err = tx.QueryRow(ctx, "SELECT g.id,g.auth_epoch FROM auth_sessions sess JOIN device_grants g ON g.id=sess.grant_id WHERE sess.token_hash=$1 AND sess.expires_at>now() AND g.principal_id=$2", hash[:], t.PrincipalID).Scan(&t.GrantID, &t.AuthEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, denied()
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = s.revoke(ctx, tx, t); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.RevokeCurrentDeviceResponse{}, nil
}

const activeParentCondition = ` AND (g.parent_grant_id IS NULL OR EXISTS(SELECT 1 FROM device_grants parent WHERE parent.id=g.parent_grant_id AND parent.principal_id=g.principal_id AND parent.auth_epoch=p.auth_epoch AND parent.revoked_at IS NULL AND parent.expires_at>now() AND 'identity.recover'=ANY(parent.scopes)))`

func (s *AuthService) challengeTransaction(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.store.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	ok := false
	defer func() {
		if !ok {
			tx.Rollback(ctx)
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616002)"); err != nil {
		return nil, databaseError(ctx, err)
	}
	if _, err = tx.Exec(ctx, "DELETE FROM auth_challenges WHERE expires_at<=now()"); err != nil {
		return nil, databaseError(ctx, err)
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM auth_challenges WHERE consumed_at IS NULL").Scan(&count); err != nil {
		return nil, databaseError(ctx, err)
	}
	if count >= 256 {
		return nil, status.Error(codes.ResourceExhausted, "Слишком много активных challenges")
	}
	ok = true
	return tx, nil
}

type recoveryAuthority struct {
	principal    string
	root, device []byte
	epoch        int64
	expiry       time.Time
	scopes       []string
}

func recoveryParent(ctx context.Context, tx pgx.Tx, id string) (*recoveryAuthority, error) {
	a := new(recoveryAuthority)
	err := tx.QueryRow(ctx, `SELECT p.id,p.root_public_key,g.device_public_key,p.auth_epoch,g.expires_at,g.scopes
 FROM device_grants g JOIN principals p ON p.id=g.principal_id WHERE g.id=$1 AND g.revoked_at IS NULL AND g.expires_at>now() AND g.auth_epoch=p.auth_epoch AND g.parent_grant_id IS NULL AND 'identity.recover'=ANY(g.scopes) FOR SHARE OF g`, id).Scan(&a.principal, &a.root, &a.device, &a.epoch, &a.expiry, &a.scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, denied()
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	m, err := membership(ctx, tx, a.principal, true)
	if err != nil {
		return nil, err
	}
	if m.Blocked {
		return nil, forbidden()
	}
	return a, nil
}

func (s *AuthService) createRecoveryChallenge(ctx context.Context, req *pb.CreateChallengeRequest) (*pb.CreateChallengeResponse, error) {
	if req.Recovery || len(req.RootPublicKey) != 0 || len(req.RecoveryGrantId) != 46 {
		return nil, status.Error(codes.InvalidArgument, "Нужно разрешение ключа восстановления")
	}
	if req.Purpose == "device.delegate" && (len(req.DevicePublicKey) != 32 || req.GrantId != "") {
		return nil, status.Error(codes.InvalidArgument, "Нужен новый публичный ключ устройства")
	}
	if req.Purpose == "recovery.device.revoke" && (len(req.DevicePublicKey) != 0 || len(req.GrantId) != 46 || req.Administrative || req.GrantId == req.RecoveryGrantId) {
		return nil, status.Error(codes.InvalidArgument, "Нужно другое разрешение устройства для отзыва")
	}
	tx, err := s.challengeTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	parent, err := recoveryParent(ctx, tx, req.RecoveryGrantId)
	if err != nil {
		return nil, err
	}
	if req.PairingId != "" {
		if err = s.validatePairChallenge(ctx, tx, &pb.CreateChallengeRequest{PairingId: req.PairingId, RootPublicKey: parent.root, DevicePublicKey: req.DevicePublicKey, Administrative: req.Administrative}, parent.principal); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	t := authn.Transcript{AuthEpoch: parent.epoch, AuthorizerGrantID: req.RecoveryGrantId, PairingID: req.PairingId, PrincipalID: parent.principal, RootPublicKey: base64.RawURLEncoding.EncodeToString(parent.root), Purpose: req.Purpose, Origin: s.origin, ServerID: s.serverID, Version: 1, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix()}
	if req.Purpose == "device.delegate" {
		t.GrantID, err = randomString("dg_")
		if err != nil {
			return nil, err
		}
		t.DevicePublicKey = base64.RawURLEncoding.EncodeToString(req.DevicePublicKey)
		t.Scopes = []string{"chat.read", "chat.write"}
		if req.Administrative {
			if !slices.Contains(parent.scopes, "space.manage") {
				return nil, forbidden()
			}
			t.Scopes = append(t.Scopes, "space.manage")
		}
		t.GrantExpiresAt = min(now.Add(30*24*time.Hour).Unix(), parent.expiry.Unix())
	} else {
		var key []byte
		var expiry time.Time
		err = tx.QueryRow(ctx, "SELECT device_public_key,expires_at,scopes FROM device_grants WHERE id=$1 AND principal_id=$2 AND revoked_at IS NULL AND expires_at>now()", req.GrantId, parent.principal).Scan(&key, &expiry, &t.Scopes)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, denied()
		}
		if err != nil {
			return nil, databaseError(ctx, err)
		}
		t.GrantID = req.GrantId
		t.DevicePublicKey = base64.RawURLEncoding.EncodeToString(key)
		t.GrantExpiresAt = expiry.Unix()
	}
	if t.GrantExpiresAt <= t.ExpiresAt {
		return nil, denied()
	}
	t.ChallengeID, err = randomString("ac_")
	if err != nil {
		return nil, err
	}
	t.Nonce, err = randomString("")
	if err != nil {
		return nil, err
	}
	canonical, err := t.Canonical()
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO auth_challenges(id,purpose,transcript,verification_key,expires_at) VALUES($1,$2,$3,$4,$5)", t.ChallengeID, t.Purpose, canonical, parent.device, time.Unix(t.ExpiresAt, 0)); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CreateChallengeResponse{ChallengeId: t.ChallengeID, Transcript: canonical}, nil
}

func (s *AuthService) completeRecovery(ctx context.Context, tx pgx.Tx, t authn.Transcript, canonical, signature []byte) error {
	if t.PairingID != "" {
		if err := lockPairApproval(ctx, tx, t); err != nil {
			return err
		}
	}
	// Один порядок регистрации устройств одного principal и проверки родителя.
	if _, err := tx.Exec(ctx, "SELECT id FROM principals WHERE id=$1 FOR UPDATE", t.PrincipalID); err != nil {
		return databaseError(ctx, err)
	}
	parent, err := recoveryParent(ctx, tx, t.AuthorizerGrantID)
	if err != nil {
		return err
	}
	if parent.principal != t.PrincipalID || parent.epoch != t.AuthEpoch || base64.RawURLEncoding.EncodeToString(parent.root) != t.RootPublicKey {
		return denied()
	}
	if t.Purpose == "recovery.device.revoke" {
		return s.revoke(ctx, tx, t)
	}
	if !slices.Equal(t.Scopes, []string{"chat.read", "chat.write"}) && !slices.Equal(t.Scopes, []string{"chat.read", "chat.write", "space.manage"}) {
		return forbidden()
	}
	if slices.Contains(t.Scopes, "space.manage") && !slices.Contains(parent.scopes, "space.manage") {
		return forbidden()
	}
	if t.GrantExpiresAt > parent.expiry.Unix() || t.GrantExpiresAt > time.Now().Add(30*24*time.Hour).Unix() {
		return denied()
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM device_grants WHERE principal_id=$1 AND revoked_at IS NULL AND expires_at>now()", t.PrincipalID).Scan(&count); err != nil {
		return databaseError(ctx, err)
	}
	if count >= 32 {
		return status.Error(codes.ResourceExhausted, "Лимит: 32 активных устройства")
	}
	device, err := base64.RawURLEncoding.DecodeString(t.DevicePublicKey)
	if err != nil || len(device) != 32 {
		return denied()
	}
	_, err = tx.Exec(ctx, `INSERT INTO device_grants(id,principal_id,device_public_key,auth_epoch,expires_at,registration_transcript,root_signature,scopes,parent_grant_id,signature_kind) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'recovery')`, t.GrantID, t.PrincipalID, device, t.AuthEpoch, time.Unix(t.GrantExpiresAt, 0), canonical, signature, t.Scopes, t.AuthorizerGrantID)
	if err != nil {
		return databaseError(ctx, err)
	}
	if t.PairingID != "" {
		return markPairApproved(ctx, tx, t.PairingID, t.GrantID)
	}
	return nil
}

func (s *AuthService) ListDevices(ctx context.Context, _ *pb.ListDevicesRequest) (*pb.ListDevicesResponse, error) {
	rows, err := s.store.pool.Query(ctx, "SELECT id,device_public_key,scopes,expires_at,revoked_at IS NOT NULL,COALESCE(parent_grant_id,''),'identity.recover'=ANY(scopes) FROM device_grants WHERE principal_id=$1 ORDER BY expires_at DESC,id LIMIT 100", authn.Actor(ctx))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := new(pb.ListDevicesResponse)
	for rows.Next() {
		v := new(pb.DeviceGrant)
		var expiry time.Time
		if err = rows.Scan(&v.Id, &v.PublicKey, &v.Scopes, &expiry, &v.Revoked, &v.ParentGrantId, &v.Recovery); err != nil {
			return nil, databaseError(ctx, err)
		}
		v.ExpiresAt = expiry.Unix()
		result.Devices = append(result.Devices, v)
	}
	if err = rows.Err(); err != nil {
		return nil, databaseError(ctx, err)
	}
	return result, nil
}
