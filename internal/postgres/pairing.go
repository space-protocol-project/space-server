package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"slices"
	"strings"
	"time"
)

const activePairCondition = ` AND NOT EXISTS(SELECT 1 FROM device_pairings pairing WHERE pairing.grant_id=g.id AND pairing.state<>'claimed' AND (pairing.expires_at<=now() OR pairing.state='cancelled'))`
const pairColumns = "id,device_name,device_public_key,administrative,created_at,expires_at,state,COALESCE(grant_id,''),proposed_root_public_key"

func readPair(row pgx.Row) (*pb.Pairing, string, error) {
	p := new(pb.Pairing)
	var created, expiry time.Time
	var grant string
	err := row.Scan(&p.Id, &p.DeviceName, &p.PublicKey, &p.Administrative, &created, &expiry, &p.State, &grant, &p.ProposedRootPublicKey)
	p.CreatedAt = created.Unix()
	p.ExpiresAt = expiry.Unix()
	return p, grant, err
}
func pairError(ctx context.Context, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return status.Error(codes.NotFound, "Код сопряжения недействителен или уже использован")
	}
	return databaseError(ctx, err)
}
func pairByID(ctx context.Context, q rowQuery, id, lock string) (*pb.Pairing, string, error) {
	p, g, err := readPair(q.QueryRow(ctx, "SELECT "+pairColumns+" FROM device_pairings WHERE id=$1 "+lock, id))
	if err != nil {
		return nil, "", pairError(ctx, err)
	}
	return p, g, nil
}
func pairSecret(ctx context.Context, q rowQuery, token string, poll bool, lock string) (*pb.Pairing, string, error) {
	prefix, column := "pc_", "code_hash"
	if poll {
		prefix, column = "pt_", "poll_hash"
	}
	if len(token) != 46 || !strings.HasPrefix(token, prefix) {
		return nil, "", status.Error(codes.InvalidArgument, "Неверный формат кода сопряжения")
	}
	hash := sha256.Sum256([]byte(token))
	p, g, err := readPair(q.QueryRow(ctx, "SELECT "+pairColumns+" FROM device_pairings WHERE "+column+"=$1 AND expires_at>now() AND state<>'claimed' "+lock, hash[:]))
	if err != nil {
		return nil, "", pairError(ctx, err)
	}
	return p, g, nil
}
func (s *AuthService) CreatePairing(ctx context.Context, req *pb.CreatePairingRequest) (*pb.CreatePairingResponse, error) {
	if len(req.PublicKey) != 32 || len(req.DeviceName) > 256 || strings.TrimSpace(req.DeviceName) == "" {
		return nil, status.Error(codes.InvalidArgument, "Нужны Ed25519 public key и имя устройства до 256 байт")
	}
	tx, err := s.store.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616004)"); err != nil {
		return nil, databaseError(ctx, err)
	}
	// Просроченное неподтверждённое разрешение никогда не становится обычным grant после очистки.
	if _, err = tx.Exec(ctx, "UPDATE device_grants SET revoked_at=now() WHERE id IN (SELECT grant_id FROM device_pairings WHERE state<>'claimed' AND expires_at<=now()) AND revoked_at IS NULL"); err != nil {
		return nil, databaseError(ctx, err)
	}
	if _, err = tx.Exec(ctx, "DELETE FROM auth_sessions WHERE grant_id IN (SELECT grant_id FROM device_pairings WHERE state<>'claimed' AND expires_at<=now())"); err != nil {
		return nil, databaseError(ctx, err)
	}
	if _, err = tx.Exec(ctx, "DELETE FROM device_pairings WHERE expires_at<=now() AND grant_id IS NULL"); err != nil {
		return nil, databaseError(ctx, err)
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM device_pairings WHERE state IN ('pending','approved') AND expires_at>now()").Scan(&count); err != nil {
		return nil, databaseError(ctx, err)
	}
	if count >= 64 {
		return nil, status.Error(codes.ResourceExhausted, "Лимит: 64 ожидающих сопряжения")
	}
	id, err := randomString("pr_")
	if err != nil {
		return nil, err
	}
	code, err := randomString("pc_")
	if err != nil {
		return nil, err
	}
	poll, err := randomString("pt_")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	p := &pb.Pairing{Id: id, DeviceName: req.DeviceName, PublicKey: req.PublicKey, Administrative: req.Administrative, CreatedAt: now.Unix(), ExpiresAt: now.Add(5 * time.Minute).Unix(), State: "pending"}
	codeHash, pollHash := sha256.Sum256([]byte(code)), sha256.Sum256([]byte(poll))
	if _, err = tx.Exec(ctx, "INSERT INTO device_pairings(id,code_hash,poll_hash,device_public_key,device_name,administrative,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", id, codeHash[:], pollHash[:], req.PublicKey, req.DeviceName, req.Administrative, now, time.Unix(p.ExpiresAt, 0)); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CreatePairingResponse{Pairing: p, Code: code, PollToken: poll}, nil
}
func (s *AuthService) InspectPairing(ctx context.Context, req *pb.InspectPairingRequest) (*pb.InspectPairingResponse, error) {
	p, _, err := pairSecret(ctx, s.store.pool, req.Code, false, "")
	if err != nil {
		return nil, err
	}
	return &pb.InspectPairingResponse{Pairing: p}, nil
}
func (s *AuthService) PollPairing(ctx context.Context, req *pb.PollPairingRequest) (*pb.PollPairingResponse, error) {
	p, grant, err := pairSecret(ctx, s.store.pool, req.PollToken, true, "")
	if err != nil {
		return nil, err
	}
	result := &pb.PollPairingResponse{Pairing: p, RootPublicKey: p.ProposedRootPublicKey}
	if p.State == "approved" {
		err = s.store.pool.QueryRow(ctx, `SELECT g.id,p.root_public_key,g.registration_transcript,g.root_signature,COALESCE(g.parent_grant_id,'') FROM device_grants g JOIN principals p ON p.id=g.principal_id WHERE g.id=$1 AND g.revoked_at IS NULL AND g.expires_at>now() AND g.auth_epoch=p.auth_epoch`+activeParentCondition+activePairCondition, grant).Scan(&result.GrantId, &result.RootPublicKey, &result.Transcript, &result.Signature, &result.ParentGrantId)
		if err != nil {
			return nil, pairError(ctx, err)
		}
		if result.ParentGrantId != "" {
			if err = s.store.pool.QueryRow(ctx, "SELECT registration_transcript,root_signature FROM device_grants WHERE id=$1 AND signature_kind='root' AND parent_grant_id IS NULL", result.ParentGrantId).Scan(&result.ParentTranscript, &result.ParentSignature); err != nil {
				return nil, pairError(ctx, err)
			}
		}
		rows, err := s.store.pool.Query(ctx, `SELECT r.transcript,r.old_signature,r.new_signature FROM root_rotations r JOIN principals p ON p.id=r.principal_id WHERE p.root_public_key=$1 AND r.completed_at IS NOT NULL ORDER BY (convert_from(r.transcript,'UTF8')::jsonb->>'auth_epoch')::bigint LIMIT 17`, result.RootPublicKey)
		if err != nil {
			return nil, databaseError(ctx, err)
		}
		defer rows.Close()
		for rows.Next() {
			proof := new(pb.RootHistoryProof)
			if err = rows.Scan(&proof.Transcript, &proof.OldSignature, &proof.NewSignature); err != nil {
				return nil, databaseError(ctx, err)
			}
			result.RootHistory = append(result.RootHistory, proof)
		}
		if err = rows.Err(); err != nil {
			return nil, databaseError(ctx, err)
		}
	}
	return result, nil
}
func validPair(p *pb.Pairing) bool { return p.State == "pending" && p.ExpiresAt > time.Now().Unix() }
func (s *AuthService) validatePairChallenge(ctx context.Context, tx pgx.Tx, req *pb.CreateChallengeRequest, principal string) error {
	p, _, err := pairByID(ctx, tx, req.PairingId, "FOR SHARE")
	if err != nil {
		return err
	}
	if !validPair(p) || !bytes.Equal(p.PublicKey, req.DevicePublicKey) || !bytes.Equal(p.ProposedRootPublicKey, req.RootPublicKey) || req.Administrative != p.Administrative {
		return status.Error(codes.FailedPrecondition, "Запрос сопряжения изменён, истёк или уже подтверждён")
	}
	var known bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1)", principal).Scan(&known); err != nil {
		return databaseError(ctx, err)
	}
	if !known {
		return forbidden()
	}
	return nil
}
func lockPairApproval(ctx context.Context, tx pgx.Tx, t authn.Transcript) error {
	p, _, err := pairByID(ctx, tx, t.PairingID, "FOR UPDATE")
	if err != nil {
		return err
	}
	key, err := base64.RawURLEncoding.DecodeString(t.DevicePublicKey)
	if err != nil || len(key) != 32 {
		return denied()
	}
	root, _ := base64.RawURLEncoding.DecodeString(t.RootPublicKey)
	expected := []string{"chat.read", "chat.write"}
	if p.Administrative {
		expected = append(expected, "space.manage")
	}
	if !validPair(p) || !bytes.Equal(p.PublicKey, key) || !bytes.Equal(p.ProposedRootPublicKey, root) || !slices.Equal(t.Scopes, expected) {
		return status.Error(codes.FailedPrecondition, "Сопряжение истекло или уже использовано")
	}
	return nil
}
func markPairApproved(ctx context.Context, tx pgx.Tx, id, grant string) error {
	if _, err := tx.Exec(ctx, "UPDATE device_pairings SET state='approved',grant_id=$2 WHERE id=$1", id, grant); err != nil {
		return databaseError(ctx, err)
	}
	return nil
}
func (s *AuthService) registerPair(ctx context.Context, tx pgx.Tx, t authn.Transcript, canonical, signature []byte) error {
	if t.AuthorizerGrantID != "" {
		return denied()
	}
	if err := lockPairApproval(ctx, tx, t); err != nil {
		return err
	}
	if err := s.register(ctx, tx, t, canonical, signature, ""); err != nil {
		return err
	}
	return markPairApproved(ctx, tx, t.PairingID, t.GrantID)
}
func (s *AuthService) CancelPairing(ctx context.Context, req *pb.CancelPairingRequest) (*pb.CancelPairingResponse, error) {
	tx, err := s.store.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	p, grant, err := pairSecret(ctx, tx, req.PollToken, true, "FOR UPDATE")
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "UPDATE device_pairings SET state='cancelled',code_hash=NULL WHERE id=$1", p.Id); err != nil {
		return nil, databaseError(ctx, err)
	}
	if grant != "" {
		if _, err = tx.Exec(ctx, "UPDATE device_grants SET revoked_at=now() WHERE id=$1", grant); err != nil {
			return nil, databaseError(ctx, err)
		}
		if _, err = tx.Exec(ctx, "DELETE FROM auth_sessions WHERE grant_id=$1", grant); err != nil {
			return nil, databaseError(ctx, err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CancelPairingResponse{}, nil
}
func (s *AuthService) ClaimPairing(ctx context.Context, req *pb.ClaimPairingRequest) (*pb.ClaimPairingResponse, error) {
	tx, err := s.store.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	p, grant, err := pairByID(ctx, tx, req.PairingId, "FOR UPDATE")
	if err != nil {
		return nil, err
	}
	if p.State != "claimed" && (p.State != "approved" || p.ExpiresAt <= time.Now().Unix()) {
		return nil, status.Error(codes.FailedPrecondition, "Сопряжение истекло или отменено")
	}
	hash := sha256.Sum256([]byte(authn.Token(ctx)))
	var actual string
	err = tx.QueryRow(ctx, "SELECT grant_id FROM auth_sessions WHERE token_hash=$1 AND expires_at>now()", hash[:]).Scan(&actual)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, denied()
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if actual != grant {
		return nil, forbidden()
	}
	if err = s.store.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "UPDATE device_pairings SET state='claimed',code_hash=NULL,poll_hash=NULL WHERE id=$1", p.Id); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.ClaimPairingResponse{}, nil
}
func (s *AuthService) ProposePairing(ctx context.Context, req *pb.ProposePairingRequest) (*pb.ProposePairingResponse, error) {
	tx, err := s.store.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	p, _, err := pairByID(ctx, tx, req.PairingId, "FOR UPDATE")
	if err != nil {
		return nil, err
	}
	if !validPair(p) {
		return nil, status.Error(codes.FailedPrecondition, "Сопряжение истекло или уже подтверждено")
	}
	if err = s.store.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	if err = chatAccess(ctx, tx, false, true); err != nil {
		return nil, err
	}
	var root []byte
	if err = tx.QueryRow(ctx, "SELECT root_public_key FROM principals WHERE id=$1", authn.Actor(ctx)).Scan(&root); err != nil {
		return nil, databaseError(ctx, err)
	}
	if len(p.ProposedRootPublicKey) > 0 && !bytes.Equal(root, p.ProposedRootPublicKey) {
		return nil, forbidden()
	}
	if _, err = tx.Exec(ctx, "UPDATE device_pairings SET proposed_root_public_key=$2 WHERE id=$1", p.Id, root); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.ProposePairingResponse{}, nil
}
