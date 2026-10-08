package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AuthService struct {
	pb.UnimplementedAuthServiceServer
	store    *Store
	serverID string
	origin   string
}

func NewAuth(store *Store, serverID, origin string) *AuthService {
	return &AuthService{store: store, serverID: serverID, origin: origin}
}
func (s *AuthService) Logout(ctx context.Context, _ *pb.LogoutRequest) (*pb.LogoutResponse, error) {
	token := authn.Token(ctx)
	if token == "" {
		return nil, denied()
	}
	hash := sha256.Sum256([]byte(token))
	if _, err := s.store.pool.Exec(ctx, "DELETE FROM auth_sessions WHERE token_hash=$1", hash[:]); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.LogoutResponse{}, nil
}
func randomString(prefix string) (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(random), nil
}
func denied() error {
	return status.Error(codes.Unauthenticated, "Challenge, подпись или разрешение устройства недействительны")
}

func (s *AuthService) CreateChallenge(ctx context.Context, req *pb.CreateChallengeRequest) (*pb.CreateChallengeResponse, error) {
	if req.PairingId != "" && ((req.Purpose != "device.register" && req.Purpose != "device.delegate") || req.Recovery) {
		return nil, status.Error(codes.InvalidArgument, "Неверный purpose сопряжения")
	}
	if req.Purpose == "device.delegate" || req.Purpose == "recovery.device.revoke" {
		return s.createRecoveryChallenge(ctx, req)
	}
	if req.RecoveryGrantId != "" || (req.Recovery && req.Purpose != "device.register") {
		return nil, status.Error(codes.InvalidArgument, "Неверное использование recovery fields")
	}
	if req.Administrative && req.Purpose != "device.register" {
		return nil, status.Error(codes.InvalidArgument, "Administrative разрешён только при регистрации устройства")
	}
	if req.Purpose != "device.register" && req.Purpose != "auth.login" && req.Purpose != "device.revoke" {
		return nil, status.Error(codes.InvalidArgument, "Неизвестный purpose")
	}
	if req.Purpose == "device.register" && (len(req.RootPublicKey) != 32 || len(req.DevicePublicKey) != 32 || req.GrantId != "") {
		return nil, status.Error(codes.InvalidArgument, "Регистрация требует два публичных ключа Ed25519")
	}
	if req.Purpose != "device.register" && (req.GrantId == "" || len(req.GrantId) > 128 || len(req.DevicePublicKey) != 0) {
		return nil, status.Error(codes.InvalidArgument, "Нужен grant_id без device_public_key")
	}
	if req.Purpose == "auth.login" && len(req.RootPublicKey) != 0 {
		return nil, status.Error(codes.InvalidArgument, "Вход использует зарегистрированное устройство")
	}
	if req.Purpose == "device.revoke" && len(req.RootPublicKey) != 32 {
		return nil, status.Error(codes.InvalidArgument, "Отзыв требует root_public_key")
	}
	tx, err := s.challengeTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	now := time.Now().UTC().Truncate(time.Second)
	transcript := authn.Transcript{Purpose: req.Purpose, Origin: s.origin, ServerID: s.serverID, Version: 1, AuthEpoch: 1, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Scopes: []string{"chat.read", "chat.write"}}
	root, device := req.RootPublicKey, req.DevicePublicKey
	if req.Purpose == "device.register" {
		if req.Administrative {
			transcript.Scopes = append(transcript.Scopes, "space.manage")
		}
		transcript.PrincipalID, transcript.AuthEpoch, err = principalForRoot(ctx, tx, root)
		if err != nil {
			return nil, err
		}
		if req.PairingId != "" {
			if err = s.validatePairChallenge(ctx, tx, req, transcript.PrincipalID); err != nil {
				return nil, err
			}
			transcript.PairingID = req.PairingId
		}
		err = tx.QueryRow(ctx, "SELECT auth_epoch FROM principals WHERE id=$1", transcript.PrincipalID).Scan(&transcript.AuthEpoch)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, databaseError(ctx, err)
		}
		transcript.GrantID, err = randomString("dg_")
		if err != nil {
			return nil, err
		}
		transcript.GrantExpiresAt = now.Add(30 * 24 * time.Hour).Unix()
		if req.Recovery {
			transcript.Scopes = append(transcript.Scopes, "identity.recover")
			transcript.GrantExpiresAt = now.Add(recoveryLifetime).Unix()
		}
	} else {
		var grantExpiry time.Time
		err = tx.QueryRow(ctx, `SELECT p.id,p.root_public_key,g.device_public_key,p.auth_epoch,g.expires_at,g.scopes FROM device_grants g JOIN principals p ON p.id=g.principal_id
   WHERE g.id=$1 AND g.revoked_at IS NULL AND g.expires_at>now() AND g.auth_epoch=p.auth_epoch`+activeParentCondition+activePairCondition, req.GrantId).Scan(&transcript.PrincipalID, &root, &device, &transcript.AuthEpoch, &grantExpiry, &transcript.Scopes)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, denied()
		}
		if err != nil {
			return nil, databaseError(ctx, err)
		}
		if req.Purpose == "device.revoke" && !bytes.Equal(req.RootPublicKey, root) {
			return nil, denied()
		}
		transcript.GrantID = req.GrantId
		transcript.GrantExpiresAt = grantExpiry.Unix()
	}
	transcript.RootPublicKey = base64.RawURLEncoding.EncodeToString(root)
	transcript.DevicePublicKey = base64.RawURLEncoding.EncodeToString(device)
	transcript.ChallengeID, err = randomString("ac_")
	if err != nil {
		return nil, err
	}
	transcript.Nonce, err = randomString("")
	if err != nil {
		return nil, err
	}
	canonical, err := transcript.Canonical()
	if err != nil {
		return nil, err
	}
	key := root
	if req.Purpose == "auth.login" {
		key = device
	}
	_, err = tx.Exec(ctx, "INSERT INTO auth_challenges(id,purpose,transcript,verification_key,expires_at) VALUES($1,$2,$3,$4,$5)", transcript.ChallengeID, req.Purpose, canonical, key, time.Unix(transcript.ExpiresAt, 0))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CreateChallengeResponse{ChallengeId: transcript.ChallengeID, Transcript: canonical}, nil
}

func (s *AuthService) CompleteChallenge(ctx context.Context, req *pb.CompleteChallengeRequest) (*pb.CompleteChallengeResponse, error) {
	if len(req.Signature) != ed25519.SignatureSize || req.ChallengeId == "" || len(req.ChallengeId) > 128 {
		return nil, denied()
	}
	tx, err := s.store.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	var canonical, key []byte
	err = tx.QueryRow(ctx, "SELECT transcript,verification_key FROM auth_challenges WHERE id=$1 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE", req.ChallengeId).Scan(&canonical, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, denied()
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	var transcript authn.Transcript
	if err = json.Unmarshal(canonical, &transcript); err != nil {
		return nil, databaseError(ctx, err)
	}
	signing, err := transcript.SigningBytes()
	if err != nil {
		return nil, denied()
	}
	if !ed25519.Verify(key, signing, req.Signature) {
		return nil, denied()
	}
	if req.InvitationToken != "" && transcript.Purpose != "device.register" {
		return nil, status.Error(codes.InvalidArgument, "Приглашение разрешено только при регистрации устройства")
	}
	result := &pb.CompleteChallengeResponse{GrantId: transcript.GrantID, PrincipalId: transcript.PrincipalID}
	switch transcript.Purpose {
	case "device.register":
		// Root-регистрация и ротация берут registry lock до строки pairing.
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616003)"); err != nil {
			return nil, databaseError(ctx, err)
		}
		if transcript.PairingID != "" {
			if req.InvitationToken != "" {
				return nil, status.Error(codes.InvalidArgument, "Сопряжение не использует приглашение")
			}
			err = s.registerPair(ctx, tx, transcript, canonical, req.Signature)
		} else {
			err = s.register(ctx, tx, transcript, canonical, req.Signature, req.InvitationToken)
		}
	case "auth.login":
		err = s.login(ctx, tx, transcript, result)
	case "device.revoke":
		err = s.revoke(ctx, tx, transcript)
	case "device.delegate", "recovery.device.revoke":
		err = s.completeRecovery(ctx, tx, transcript, canonical, req.Signature)
	default:
		return nil, denied()
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "UPDATE auth_challenges SET consumed_at=now() WHERE id=$1", req.ChallengeId); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return result, nil
}

func (s *AuthService) register(ctx context.Context, tx pgx.Tx, t authn.Transcript, canonical, signature []byte, invitation string) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616003)"); err != nil {
		return databaseError(ctx, err)
	}
	var policy string
	if err := tx.QueryRow(ctx, "SELECT registration_policy FROM space_settings WHERE singleton=true FOR SHARE").Scan(&policy); err != nil {
		return databaseError(ctx, err)
	}
	var existing bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1)", t.PrincipalID).Scan(&existing); err != nil {
		return databaseError(ctx, err)
	}
	if !existing && policy == "closed" && invitation == "" {
		return status.Error(codes.PermissionDenied, "Регистрация новых идентичностей закрыта")
	}
	root, _ := base64.RawURLEncoding.DecodeString(t.RootPublicKey)
	principal, currentEpoch, err := principalForRoot(ctx, tx, root)
	if err != nil {
		return err
	}
	if principal != t.PrincipalID || currentEpoch != t.AuthEpoch {
		return denied()
	}
	device, _ := base64.RawURLEncoding.DecodeString(t.DevicePublicKey)
	if _, err := tx.Exec(ctx, "INSERT INTO principals(id,root_public_key) VALUES($1,$2) ON CONFLICT(id) DO NOTHING", t.PrincipalID, root); err != nil {
		return databaseError(ctx, err)
	}
	var epoch int64
	if err := tx.QueryRow(ctx, "SELECT auth_epoch FROM principals WHERE id=$1 FOR UPDATE", t.PrincipalID).Scan(&epoch); err != nil {
		return databaseError(ctx, err)
	}
	if epoch != t.AuthEpoch {
		return denied()
	}
	if _, err := tx.Exec(ctx, "INSERT INTO principal_roots(public_key,principal_id,auth_epoch) VALUES($1,$2,$3) ON CONFLICT(public_key) DO NOTHING", root, t.PrincipalID, epoch); err != nil {
		return databaseError(ctx, err)
	}
	if slices.Contains(t.Scopes, "identity.recover") {
		var recoveryCount int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM device_grants WHERE principal_id=$1 AND revoked_at IS NULL AND expires_at>now() AND 'identity.recover'=ANY(scopes)", t.PrincipalID).Scan(&recoveryCount); err != nil {
			return databaseError(ctx, err)
		}
		if recoveryCount > 0 {
			return status.Error(codes.FailedPrecondition, "Сначала отзовите прежний ключ восстановления")
		}
	}
	if invitation != "" {
		if _, err := redeemInvite(ctx, tx, invitation, t.PrincipalID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, "INSERT INTO memberships(principal_id,role) VALUES($1,'member') ON CONFLICT(principal_id) DO NOTHING", t.PrincipalID); err != nil {
			return databaseError(ctx, err)
		}
	}
	member, memberErr := membership(ctx, tx, t.PrincipalID, true)
	if memberErr != nil {
		return memberErr
	}
	if member.Blocked {
		return forbidden()
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM device_grants WHERE principal_id=$1 AND revoked_at IS NULL AND expires_at>now()", t.PrincipalID).Scan(&count); err != nil {
		return databaseError(ctx, err)
	}
	if count >= 32 {
		return status.Error(codes.ResourceExhausted, "Лимит: 32 активных устройства")
	}
	_, err = tx.Exec(ctx, "INSERT INTO device_grants(id,principal_id,device_public_key,auth_epoch,expires_at,registration_transcript,root_signature,scopes) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", t.GrantID, t.PrincipalID, device, t.AuthEpoch, time.Unix(t.GrantExpiresAt, 0), canonical, signature, t.Scopes)
	if err != nil {
		return databaseError(ctx, err)
	}
	return nil
}

func (s *AuthService) login(ctx context.Context, tx pgx.Tx, t authn.Transcript, result *pb.CompleteChallengeResponse) error {
	if err := activeGrant(ctx, tx, t); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM auth_sessions WHERE expires_at<=now()"); err != nil {
		return databaseError(ctx, err)
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM auth_sessions WHERE grant_id=$1", t.GrantID).Scan(&count); err != nil {
		return databaseError(ctx, err)
	}
	if count >= 64 {
		return status.Error(codes.ResourceExhausted, "Лимит: 64 сессии устройства")
	}
	token, err := randomString("st_")
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(token))
	expiry := min(time.Now().Add(10*time.Minute).Unix(), t.GrantExpiresAt)
	if _, err := tx.Exec(ctx, "INSERT INTO auth_sessions(token_hash,grant_id,expires_at) VALUES($1,$2,$3)", hash[:], t.GrantID, time.Unix(expiry, 0)); err != nil {
		return databaseError(ctx, err)
	}
	result.AccessToken = token
	result.ExpiresAt = expiry
	return nil
}

func activeGrant(ctx context.Context, tx pgx.Tx, t authn.Transcript) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT g.id FROM device_grants g JOIN principals p ON p.id=g.principal_id WHERE g.id=$1 AND p.id=$2 AND p.auth_epoch=$3 AND g.auth_epoch=p.auth_epoch AND g.revoked_at IS NULL AND g.expires_at>now() `+activeParentCondition+activePairCondition+` FOR UPDATE OF g`, t.GrantID, t.PrincipalID, t.AuthEpoch).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return denied()
	}
	if err != nil {
		return databaseError(ctx, err)
	}
	return nil
}

func (s *AuthService) revoke(ctx context.Context, tx pgx.Tx, t authn.Transcript) error {
	if err := activeGrant(ctx, tx, t); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE device_grants SET revoked_at=now() WHERE id=$1 OR parent_grant_id=$1", t.GrantID); err != nil {
		return databaseError(ctx, err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM auth_sessions WHERE grant_id IN (SELECT id FROM device_grants WHERE id=$1 OR parent_grant_id=$1)", t.GrantID); err != nil {
		return databaseError(ctx, err)
	}
	return nil
}

const sessionQuery = `SELECT p.id FROM auth_sessions s JOIN device_grants g ON g.id=s.grant_id JOIN principals p ON p.id=g.principal_id
 WHERE s.token_hash=$1 AND s.expires_at>now() AND g.expires_at>now() AND g.revoked_at IS NULL AND g.auth_epoch=p.auth_epoch` + activeParentCondition + activePairCondition

func (s *Store) Authenticate(ctx context.Context, token string) (string, error) {
	if len(token) != 46 {
		return "", denied()
	}
	hash := sha256.Sum256([]byte(token))
	var id string
	err := s.pool.QueryRow(ctx, sessionQuery, hash[:]).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", denied()
	}
	if err != nil {
		return "", databaseError(ctx, err)
	}
	return id, nil
}

func (s *Store) lockSession(ctx context.Context, tx pgx.Tx) error {
	token := authn.Token(ctx)
	if token == "" {
		return nil
	} // Только прямые внутренние тесты и явный demo без interceptor.
	hash := sha256.Sum256([]byte(token))
	var id string
	err := tx.QueryRow(ctx, sessionQuery+" FOR SHARE OF g", hash[:]).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return denied()
	}
	if err != nil {
		return databaseError(ctx, err)
	}
	return nil
}
