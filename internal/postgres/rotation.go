package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	rotation "github.com/space-protocol-project/space-server/internal/identityrotation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Старый root остаётся tombstone: он не может стать новой идентичностью.
func principalForRoot(ctx context.Context, tx pgx.Tx, root []byte) (string, int64, error) {
	var principal string
	var epoch int64
	var current []byte
	err := tx.QueryRow(ctx, `SELECT p.id,p.auth_epoch,p.root_public_key FROM principal_roots h JOIN principals p ON p.id=h.principal_id WHERE h.public_key=$1`, root).Scan(&principal, &epoch, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return authn.PrincipalID(root), 1, nil
	}
	if err != nil {
		return "", 0, databaseError(ctx, err)
	}
	if !bytes.Equal(root, current) {
		return "", 0, denied()
	}
	return principal, epoch, nil
}

func rotationExpected(t rotation.Transcript) rotation.Expected {
	decode := func(v string) []byte { b, _ := base64.RawURLEncoding.DecodeString(v); return b }
	return rotation.Expected{PrincipalID: t.PrincipalID, RootPublicKey: decode(t.OldRootPublicKey), AuthEpoch: t.AuthEpoch,
		ServerID: t.ServerID, Origin: t.Origin, ChallengeID: t.ChallengeID, OperationID: t.OperationID,
		NewRootPublicKey: decode(t.NewRootPublicKey), NewDevicePublicKey: decode(t.NewDevicePublicKey), Scopes: t.Scopes, RetainedDevicesHash: t.RetainedDevicesHash}
}

func (s *AuthService) CreateRootRotation(ctx context.Context, req *pb.CreateRootRotationRequest) (*pb.CreateRootRotationResponse, error) {
	if err := validateRootRotationRequest(req); err != nil {
		return nil, err
	}
	tx, err := s.store.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	// Тот же порядок глобальной блокировки, что у регистрации и commit ротации.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616003)"); err != nil {
		return nil, databaseError(ctx, err)
	}
	hash := sha256.Sum256([]byte(authn.Token(ctx)))
	var root []byte
	var epoch int64
	var source string
	var scopes []string
	err = tx.QueryRow(ctx, `SELECT p.root_public_key,p.auth_epoch,g.id,g.scopes FROM auth_sessions sess JOIN device_grants g ON g.id=sess.grant_id JOIN principals p ON p.id=g.principal_id
 WHERE sess.token_hash=$1 AND p.id=$2 AND sess.expires_at>now() AND g.expires_at>now() AND g.revoked_at IS NULL AND g.auth_epoch=p.auth_epoch AND g.parent_grant_id IS NULL AND g.signature_kind IN ('root','rotation') AND NOT ('identity.recover'=ANY(g.scopes))`, hash[:], authn.Actor(ctx)).Scan(&root, &epoch, &source, &scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, denied()
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	member, err := membership(ctx, tx, authn.Actor(ctx), true)
	if err != nil {
		return nil, err
	}
	if member.Blocked {
		return nil, denied()
	}
	if epoch != req.ExpectedAuthEpoch || bytes.Equal(root, req.NewRootPublicKey) {
		return nil, status.Error(codes.FailedPrecondition, "Эпоха или новый root недействительны")
	}
	if err := requireUnusedRotationKeys(ctx, tx, req, source); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM root_rotations WHERE completed_at IS NULL AND expires_at<=now()"); err != nil {
		return nil, databaseError(ctx, err)
	}
	existing, err := existingRotationChallenge(ctx, tx, req, epoch)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	created, err := s.prepareRotationChallenge(ctx, tx, req, root, epoch, source, scopes)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return created, nil
}

func (s *AuthService) CompleteRootRotation(ctx context.Context, req *pb.CompleteRootRotationRequest) (*pb.CompleteRootRotationResponse, error) {
	if len(req.ChallengeId) > 64 || len(req.OldSignature) != 64 || len(req.NewSignature) != 64 {
		return nil, denied()
	}
	tx, err := s.store.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616003)"); err != nil {
		return nil, databaseError(ctx, err)
	}
	completion, err := s.loadRotationCompletion(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	t, raw, source, grant, completed := completion.transcript, completion.raw, completion.source, completion.grant, completion.completed
	result := &pb.CompleteRootRotationResponse{PrincipalId: t.PrincipalID, AuthEpoch: t.AuthEpoch + 1, GrantId: grant, Transcript: raw, OldSignature: req.OldSignature, NewSignature: req.NewSignature}
	if completed != nil {
		return result, nil
	}
	expected := rotationExpected(t)
	root, epoch, err := lockRotationAuthority(ctx, tx, t, source, expected)
	if err != nil {
		return nil, err
	}
	grant, err = applyRootRotation(ctx, tx, t, expected, root, epoch, raw, req, source)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	result.GrantId = grant
	return result, nil
}

type rotationCompletion struct {
	transcript    rotation.Transcript
	raw           []byte
	source, grant string
	completed     *time.Time
}

func (s *AuthService) loadRotationCompletion(ctx context.Context, tx pgx.Tx, req *pb.CompleteRootRotationRequest) (*rotationCompletion, error) {
	var raw []byte
	var source, grant string
	var completed *time.Time
	err := tx.QueryRow(ctx, "SELECT transcript,source_grant_id,completed_at,COALESCE(grant_id,'') FROM root_rotations WHERE id=$1 FOR UPDATE", req.ChallengeId).Scan(&raw, &source, &completed, &grant)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, denied()
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	var t rotation.Transcript
	if json.Unmarshal(raw, &t) != nil {
		return nil, denied()
	}
	if t.Origin != s.origin || t.ServerID != s.serverID || t.ChallengeID != req.ChallengeId {
		return nil, denied()
	}
	now := time.Now()
	if completed != nil {
		now = time.Unix(t.IssuedAt, 0)
	}
	if t.Verify(rotationExpected(t), now, req.OldSignature, req.NewSignature) != nil {
		return nil, denied()
	}
	return &rotationCompletion{t, raw, source, grant, completed}, nil
}

func lockRotationAuthority(ctx context.Context, tx pgx.Tx, t rotation.Transcript, source string, expected rotation.Expected) ([]byte, int64, error) {
	// Pairing → principal → grants: тот же порядок, что у approval/cancel.
	pairs, pairErr := tx.Query(ctx, "SELECT id FROM device_pairings WHERE proposed_root_public_key=$1 AND state IN ('pending','approved') ORDER BY id FOR UPDATE", expected.RootPublicKey)
	if pairErr != nil {
		return nil, 0, databaseError(ctx, pairErr)
	}
	for pairs.Next() {
	}
	pairErr = pairs.Err()
	pairs.Close()
	if pairErr != nil {
		return nil, 0, databaseError(ctx, pairErr)
	}
	var root []byte
	var epoch int64
	err := tx.QueryRow(ctx, "SELECT root_public_key,auth_epoch FROM principals WHERE id=$1 FOR UPDATE", t.PrincipalID).Scan(&root, &epoch)
	if err != nil {
		return nil, 0, databaseError(ctx, err)
	}
	if epoch != t.AuthEpoch || !bytes.Equal(root, expected.RootPublicKey) {
		return nil, 0, denied()
	}
	// До membership lock: административные действия блокируют grant перед membership.
	rows, lockErr := tx.Query(ctx, "SELECT id FROM device_grants WHERE principal_id=$1 ORDER BY id FOR UPDATE", t.PrincipalID)
	if lockErr != nil {
		return nil, 0, databaseError(ctx, lockErr)
	}
	for rows.Next() {
	}
	lockErr = rows.Err()
	rows.Close()
	if lockErr != nil {
		return nil, 0, databaseError(ctx, lockErr)
	}
	var active bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM device_grants WHERE id=$1 AND principal_id=$2 AND auth_epoch=$3 AND revoked_at IS NULL AND expires_at>now())", source, t.PrincipalID, epoch).Scan(&active); err != nil {
		return nil, 0, databaseError(ctx, err)
	}
	member, err := membership(ctx, tx, t.PrincipalID, true)
	if err != nil {
		return nil, 0, err
	}
	if !active || member.Blocked {
		return nil, 0, denied()
	}
	var used bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM principal_roots WHERE public_key=$1) OR EXISTS(SELECT 1 FROM device_grants WHERE principal_id=$3 AND device_public_key IN ($1,$2))", expected.NewRootPublicKey, expected.NewDevicePublicKey, t.PrincipalID).Scan(&used); err != nil {
		return nil, 0, databaseError(ctx, err)
	}
	if used {
		return nil, 0, status.Error(codes.AlreadyExists, "Root уже использован")
	}
	return root, epoch, nil
}

func applyRootRotation(ctx context.Context, tx pgx.Tx, t rotation.Transcript, expected rotation.Expected, root []byte, epoch int64, raw []byte, req *pb.CompleteRootRotationRequest, source string) (string, error) {
	retained, err := validateSavedRetention(ctx, tx, t, source)
	if err != nil {
		return "", err
	}

	if _, err := tx.Exec(ctx, "UPDATE device_pairings SET state='cancelled',code_hash=NULL,poll_hash=NULL WHERE proposed_root_public_key=$1 AND state IN ('pending','approved')", root); err != nil {
		return "", databaseError(ctx, err)
	}
	if _, err := tx.Exec(ctx, "UPDATE principal_roots SET retired_at=now() WHERE public_key=$1", root); err != nil {
		return "", databaseError(ctx, err)
	}
	if _, err := tx.Exec(ctx, "UPDATE principals SET root_public_key=$1,auth_epoch=auth_epoch+1 WHERE id=$2", expected.NewRootPublicKey, t.PrincipalID); err != nil {
		return "", databaseError(ctx, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO principal_roots(public_key,principal_id,auth_epoch) VALUES($1,$2,$3)", expected.NewRootPublicKey, t.PrincipalID, epoch+1); err != nil {
		return "", databaseError(ctx, err)
	}
	if _, err := tx.Exec(ctx, "UPDATE device_grants SET revoked_at=COALESCE(revoked_at,now()) WHERE principal_id=$1", t.PrincipalID); err != nil {
		return "", databaseError(ctx, err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM auth_sessions WHERE grant_id IN (SELECT id FROM device_grants WHERE principal_id=$1)", t.PrincipalID); err != nil {
		return "", databaseError(ctx, err)
	}
	if err := restoreRetainedGrants(ctx, tx, t, retained); err != nil {
		return "", err
	}
	grant, err := randomString("dg_")
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO device_grants(id,principal_id,device_public_key,auth_epoch,expires_at,registration_transcript,root_signature,scopes,signature_kind) VALUES($1,$2,$3,$4,now()+interval '30 days',$5,$6,$7,'rotation')`, grant, t.PrincipalID, expected.NewDevicePublicKey, epoch+1, raw, req.NewSignature, t.Scopes); err != nil {
		return "", databaseError(ctx, err)
	}
	if _, err := tx.Exec(ctx, "UPDATE mls_device_routes SET current_grant_id=$1 WHERE current_grant_id=$2", grant, source); err != nil {
		return "", databaseError(ctx, err)
	}
	if _, err := tx.Exec(ctx, "UPDATE root_rotations SET completed_at=now(),old_signature=$1,new_signature=$2,grant_id=$3 WHERE id=$4", req.OldSignature, req.NewSignature, grant, t.ChallengeID); err != nil {
		return "", databaseError(ctx, err)
	}
	return grant, nil
}

func (s *AuthService) prepareRotationChallenge(ctx context.Context, tx pgx.Tx, req *pb.CreateRootRotationRequest, root []byte, epoch int64, source string, scopes []string) (*pb.CreateRootRotationResponse, error) {
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM root_rotations WHERE completed_at IS NULL").Scan(&count); err != nil {
		return nil, databaseError(ctx, err)
	}
	if count >= 64 {
		return nil, status.Error(codes.ResourceExhausted, "Слишком много запросов ротации")
	}
	id, err := randomString("rc_")
	if err != nil {
		return nil, err
	}
	nonce, err := randomString("")
	if err != nil {
		return nil, err
	}
	retained, err := prepareRetention(ctx, tx, authn.Actor(ctx), source, req)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	t := rotation.Transcript{AuthEpoch: epoch, ChallengeID: id, ExpiresAt: now.Add(120 * time.Second).Unix(), IssuedAt: now.Unix(), NewDevicePublicKey: base64.RawURLEncoding.EncodeToString(req.NewDevicePublicKey), NewRootPublicKey: base64.RawURLEncoding.EncodeToString(req.NewRootPublicKey), OldRootPublicKey: base64.RawURLEncoding.EncodeToString(root), Nonce: nonce, OperationID: req.OperationId, Origin: s.origin, PrincipalID: authn.Actor(ctx), Purpose: "identity.root.rotate", Scopes: scopes, ServerID: s.serverID, V: 1}
	if len(retained) > 0 {
		t.V = 2
		t.RetainedDevicesHash = base64.RawURLEncoding.EncodeToString(req.RetainedDevicesHash)
	}
	if t.Validate(rotationExpected(t), now) != nil {
		return nil, status.Error(codes.InvalidArgument, "Неверный контракт ротации")
	}
	canonical, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	retainedRaw, _ := json.Marshal(retained)
	if _, err = tx.Exec(ctx, "INSERT INTO root_rotations(id,principal_id,operation_id,transcript,source_grant_id,expires_at,retained_devices) VALUES($1,$2,$3,$4,$5,$6,$7)", id, t.PrincipalID, t.OperationID, canonical, source, time.Unix(t.ExpiresAt, 0), retainedRaw); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CreateRootRotationResponse{ChallengeId: id, Transcript: canonical}, nil
}

func existingRotationChallenge(ctx context.Context, tx pgx.Tx, req *pb.CreateRootRotationRequest, epoch int64) (*pb.CreateRootRotationResponse, error) {
	var id string
	var canonical []byte
	err := tx.QueryRow(ctx, "SELECT id,transcript FROM root_rotations WHERE principal_id=$1 AND operation_id=$2", authn.Actor(ctx), req.OperationId).Scan(&id, &canonical)
	if err == nil {
		var saved rotation.Transcript
		if json.Unmarshal(canonical, &saved) != nil {
			return nil, denied()
		}
		if saved.NewRootPublicKey != base64.RawURLEncoding.EncodeToString(req.NewRootPublicKey) || saved.NewDevicePublicKey != base64.RawURLEncoding.EncodeToString(req.NewDevicePublicKey) || saved.AuthEpoch != epoch || saved.RetainedDevicesHash != base64.RawURLEncoding.EncodeToString(req.RetainedDevicesHash) {
			return nil, status.Error(codes.AlreadyExists, "operation_id относится к другому переходу")
		}
		return &pb.CreateRootRotationResponse{ChallengeId: id, Transcript: canonical}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, databaseError(ctx, err)
	}

	return nil, nil
}

func validateRootRotationRequest(req *pb.CreateRootRotationRequest) error {
	if !validRetentionRequest(req) || len(req.NewRootPublicKey) != 32 || len(req.NewDevicePublicKey) != 32 || len(req.OperationId) > 64 {
		return status.Error(codes.InvalidArgument, "Нужен профиль root-rotation-v1 и новые ключи")
	}
	return nil
}

func requireUnusedRotationKeys(ctx context.Context, tx pgx.Tx, req *pb.CreateRootRotationRequest, source string) error {
	if _, err := prepareRetention(ctx, tx, authn.Actor(ctx), source, req); err != nil {
		return err
	}
	var used bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM principal_roots WHERE public_key=$1) OR EXISTS(SELECT 1 FROM device_grants WHERE principal_id=$3 AND device_public_key IN ($1,$2))", req.NewRootPublicKey, req.NewDevicePublicKey, authn.Actor(ctx)).Scan(&used); err != nil {
		return databaseError(ctx, err)
	}
	if used {
		return status.Error(codes.AlreadyExists, "Root уже использован")
	}
	return nil
}

func validRetentionRequest(req *pb.CreateRootRotationRequest) bool {
	if req.Profile == "root-rotation-v1" {
		return len(req.RetainedGrantIds) == 0 && len(req.RetainedDevicesHash) == 0
	}
	if req.Profile != "root-rotation-v2" || len(req.RetainedDevicesHash) != 32 || len(req.RetainedGrantIds) < 1 || len(req.RetainedGrantIds) > 256 {
		return false
	}
	previous := ""
	for _, id := range req.RetainedGrantIds {
		if len(id) != 46 || id <= previous {
			return false
		}
		previous = id
	}
	return true
}
