package postgres

import (
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
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
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

func (s *AuthService) CreateChallenge(ctx context.Context, request *pb.CreateChallengeRequest) (*pb.CreateChallengeResponse, error) {
	if err := validatePairingPurpose(request); err != nil {
		return nil, err
	}
	if request.Purpose == "device.delegate" || request.Purpose == "recovery.device.revoke" {
		return s.createRecoveryChallenge(ctx, request)
	}
	if err := validateChallengeRequest(request); err != nil {
		return nil, err
	}

	transaction, err := s.challengeTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback(ctx)

	now := time.Now().UTC().Truncate(time.Second)
	transcript := authn.Transcript{
		Purpose: request.Purpose, Origin: s.origin, ServerID: s.serverID,
		Version: 1, AuthEpoch: 1, IssuedAt: now.Unix(),
		ExpiresAt: now.Add(time.Minute).Unix(), Scopes: []string{"chat.read", "chat.write"},
	}
	keys := challengeKeys{root: request.RootPublicKey, device: request.DevicePublicKey}
	if request.Purpose == "device.register" {
		err = s.prepareRegistrationChallenge(ctx, transaction, request, &transcript, now)
	} else {
		keys, err = s.prepareGrantChallenge(ctx, transaction, request, &transcript)
	}
	if err != nil {
		return nil, err
	}
	transcript.RootPublicKey = base64.RawURLEncoding.EncodeToString(keys.root)
	transcript.DevicePublicKey = base64.RawURLEncoding.EncodeToString(keys.device)
	canonical, err := saveChallenge(ctx, transaction, &transcript, keys)
	if err != nil {
		return nil, err
	}
	result := &pb.CreateChallengeResponse{ChallengeId: transcript.ChallengeID, Transcript: canonical}
	if request.Purpose == "auth.login" {
		result.RootHistory, err = loadRootHistory(ctx, transaction, transcript.PrincipalID)
		if err != nil {
			return nil, err
		}
		result.RetentionProof, err = loadRetentionProof(ctx, transaction, transcript.PrincipalID, transcript.GrantID, transcript.AuthEpoch)
		if err != nil {
			return nil, err
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return result, nil
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
	transcript, canonical, err := readVerifiedChallenge(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	result := &pb.CompleteChallengeResponse{GrantId: transcript.GrantID, PrincipalId: transcript.PrincipalID}
	switch transcript.Purpose {
	case "device.register":
		err = s.completeRegistration(ctx, tx, transcript, canonical, req)
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
	if err := checkRegistrationPolicy(ctx, tx, t.PrincipalID, invitation); err != nil {
		return err
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
	if err := prepareRegistrationMembership(ctx, tx, t, invitation); err != nil {
		return err
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
	// Пока выбор владельца открыт, registry lock сохраняет порядок registration → grant → settings.
	var automatic bool
	if err := tx.QueryRow(ctx, "SELECT owner_id IS NULL AND owner_claim_policy='first_login' FROM space_settings WHERE singleton=true").Scan(&automatic); err != nil {
		return databaseError(ctx, err)
	}
	if automatic {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616003)"); err != nil {
			return databaseError(ctx, err)
		}
	}
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
	if automatic {
		if err := claimFirstLoginOwner(ctx, tx, t); err != nil {
			return err
		}
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

func claimFirstLoginOwner(ctx context.Context, tx pgx.Tx, t authn.Transcript) error {
	member, err := membership(ctx, tx, t.PrincipalID, true)
	if err != nil {
		return err
	}
	if member.Blocked {
		return forbidden()
	}
	if !slices.Contains(t.Scopes, "identity.recover") {
		var revision int64
		err = tx.QueryRow(ctx, "UPDATE space_settings SET owner_id=$1,revision=revision+1,setup_code_hash=NULL,setup_expires_at=NULL WHERE singleton=true AND owner_id IS NULL AND owner_claim_policy='first_login' RETURNING revision", t.PrincipalID).Scan(&revision)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return databaseError(ctx, err)
		}
		if err == nil {
			if err = audit(ctx, tx, t.PrincipalID, "owner.first-login", revision); err != nil {
				return databaseError(ctx, err)
			}
		}
	}
	return nil
}

func prepareRegistrationMembership(ctx context.Context, tx pgx.Tx, t authn.Transcript, invitation string) error {
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
	return nil
}

func readVerifiedChallenge(ctx context.Context, tx pgx.Tx, req *pb.CompleteChallengeRequest) (authn.Transcript, []byte, error) {
	var canonical, key []byte
	err := tx.QueryRow(ctx, "SELECT transcript,verification_key FROM auth_challenges WHERE id=$1 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE", req.ChallengeId).Scan(&canonical, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return authn.Transcript{}, nil, denied()
	}
	if err != nil {
		return authn.Transcript{}, nil, databaseError(ctx, err)
	}
	var transcript authn.Transcript
	if err = json.Unmarshal(canonical, &transcript); err != nil {
		return authn.Transcript{}, nil, databaseError(ctx, err)
	}
	signing, err := transcript.SigningBytes()
	if err != nil {
		return authn.Transcript{}, nil, denied()
	}
	if !ed25519.Verify(key, signing, req.Signature) {
		return authn.Transcript{}, nil, denied()
	}
	if req.InvitationToken != "" && transcript.Purpose != "device.register" {
		return authn.Transcript{}, nil, status.Error(codes.InvalidArgument, "Приглашение разрешено только при регистрации устройства")
	}
	return transcript, canonical, nil
}

func checkRegistrationPolicy(ctx context.Context, tx pgx.Tx, principal string, invitation string) error {
	var policy string
	var bootstrap bool
	if err := tx.QueryRow(ctx, "SELECT registration_policy,owner_id IS NULL AND owner_claim_policy='first_login' FROM space_settings WHERE singleton=true FOR SHARE").Scan(&policy, &bootstrap); err != nil {
		return databaseError(ctx, err)
	}
	var existing bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1)", principal).Scan(&existing); err != nil {
		return databaseError(ctx, err)
	}
	if !existing && policy == "closed" && invitation == "" && !bootstrap {
		return status.Error(codes.PermissionDenied, "Регистрация новых идентичностей закрыта")
	}
	return nil
}

func (s *AuthService) completeRegistration(ctx context.Context, tx pgx.Tx, transcript authn.Transcript, canonical []byte, req *pb.CompleteChallengeRequest) error {
	// Root-регистрация и ротация берут registry lock до строки pairing.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616003)"); err != nil {
		return databaseError(ctx, err)
	}
	if transcript.PairingID != "" {
		if req.InvitationToken != "" {
			return status.Error(codes.InvalidArgument, "Сопряжение не использует приглашение")
		}
		return s.registerPair(ctx, tx, transcript, canonical, req.Signature)
	} else {
		return s.register(ctx, tx, transcript, canonical, req.Signature, req.InvitationToken)
	}
}
