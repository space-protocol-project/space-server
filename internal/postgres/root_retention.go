package postgres

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rotation "github.com/space-protocol-project/space-server/internal/identityrotation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const retainableGrant = `g.revoked_at IS NULL AND g.expires_at>now() AND g.auth_epoch=p.auth_epoch AND NOT ('identity.recover'=ANY(g.scopes))` + activeParentCondition + ` AND NOT EXISTS(SELECT 1 FROM device_pairings pairing WHERE pairing.grant_id=g.id AND pairing.state<>'claimed')`

func retentionSnapshot(ctx context.Context, tx pgx.Tx, principal string, ids []string) ([]rotation.RetainedDevice, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT g.id,g.device_public_key,g.scopes,g.expires_at FROM device_grants g JOIN principals p ON p.id=g.principal_id WHERE g.principal_id=$1 AND g.id=ANY($2) AND `+retainableGrant+` ORDER BY g.id COLLATE "C"`, principal, ids)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	devices := make([]rotation.RetainedDevice, 0, len(ids))
	for rows.Next() {
		var d rotation.RetainedDevice
		var key []byte
		var expiry time.Time
		if err = rows.Scan(&d.GrantID, &key, &d.Scopes, &expiry); err != nil {
			return nil, databaseError(ctx, err)
		}
		d.PublicKey = base64.RawURLEncoding.EncodeToString(key)
		d.ExpiresAt = expiry.Unix()
		devices = append(devices, d)
	}
	if rows.Err() != nil {
		return nil, databaseError(ctx, rows.Err())
	}
	if len(devices) != len(ids) {
		return nil, status.Error(codes.FailedPrecondition, "Список действующих устройств изменился")
	}
	return devices, nil
}

func prepareRetention(ctx context.Context, tx pgx.Tx, principal, source string, req *pb.CreateRootRotationRequest) ([]rotation.RetainedDevice, error) {
	if slices.Contains(req.RetainedGrantIds, source) {
		return nil, status.Error(codes.InvalidArgument, "Исходное устройство получает новый ключ отдельно")
	}
	devices, err := retentionSnapshot(ctx, tx, principal, req.RetainedGrantIds)
	if err != nil {
		return nil, err
	}
	if len(devices) > 0 {
		hash, err := rotation.RetentionRoot(devices)
		if err != nil || !bytes.Equal(hash, req.RetainedDevicesHash) {
			return nil, status.Error(codes.FailedPrecondition, "Подписываемый список устройств не совпадает")
		}
	}
	if err := requireMlsRetention(ctx, tx, principal, source, req.RetainedGrantIds); err != nil {
		return nil, err
	}
	return devices, nil
}

func requireMlsRetention(ctx context.Context, tx pgx.Tx, principal, source string, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	var missing bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mls_members m JOIN mls_device_routes r ON r.leaf_grant_id=m.grant_id JOIN device_grants g ON g.id=r.current_grant_id WHERE g.principal_id=$1 AND r.current_grant_id<>$2 AND NOT(r.current_grant_id=ANY($3::text[])) AND m.left_sequence IS NULL)`, principal, source, ids).Scan(&missing)
	if err != nil {
		return databaseError(ctx, err)
	}
	if missing {
		return status.Error(codes.FailedPrecondition, "В закрытом канале есть устройство без подтверждённого сохранения доступа")
	}
	return nil
}

func validateSavedRetention(ctx context.Context, tx pgx.Tx, t rotation.Transcript, source string) ([]rotation.RetainedDevice, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT retained_devices FROM root_rotations WHERE id=$1", t.ChallengeID).Scan(&raw); err != nil {
		return nil, databaseError(ctx, err)
	}
	var saved []rotation.RetainedDevice
	if json.Unmarshal(raw, &saved) != nil {
		return nil, denied()
	}
	ids := make([]string, len(saved))
	for i, d := range saved {
		ids[i] = d.GrantID
	}
	current, err := retentionSnapshot(ctx, tx, t.PrincipalID, ids)
	if err != nil {
		return nil, err
	}
	if len(saved) > 0 {
		hash, err := rotation.RetentionRoot(current)
		if err != nil || base64.RawURLEncoding.EncodeToString(hash) != t.RetainedDevicesHash {
			return nil, status.Error(codes.FailedPrecondition, "Ключи, права или сроки устройств изменились")
		}
	} else if t.RetainedDevicesHash != "" {
		return nil, denied()
	}
	if err = requireMlsRetention(ctx, tx, t.PrincipalID, source, ids); err != nil {
		return nil, err
	}
	return current, nil
}

func restoreRetainedGrants(ctx context.Context, tx pgx.Tx, t rotation.Transcript, devices []rotation.RetainedDevice) error {
	for _, d := range devices {
		if _, err := tx.Exec(ctx, `UPDATE device_grants SET auth_epoch=$1,revoked_at=NULL,parent_grant_id=NULL,signature_kind='retained' WHERE id=$2 AND principal_id=$3`, t.AuthEpoch+1, d.GrantID, t.PrincipalID); err != nil {
			return databaseError(ctx, err)
		}
	}
	return nil
}

func loadRetentionProof(ctx context.Context, tx pgx.Tx, principal, grant string, epoch int64) (*pb.RetentionProof, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT retained_devices FROM root_rotations WHERE principal_id=$1 AND completed_at IS NOT NULL AND (convert_from(transcript,'UTF8')::jsonb->>'auth_epoch')::bigint=$2`, principal, epoch-1).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	var devices []rotation.RetainedDevice
	if json.Unmarshal(raw, &devices) != nil {
		return nil, denied()
	}
	for i, d := range devices {
		if d.GrantID != grant {
			continue
		}
		proof, err := rotation.ProveRetention(devices, i)
		if err != nil {
			return nil, denied()
		}
		key, _ := base64.RawURLEncoding.DecodeString(d.PublicKey)
		return &pb.RetentionProof{GrantId: d.GrantID, PublicKey: key, Scopes: d.Scopes, ExpiresAt: d.ExpiresAt, Index: uint32(i), Count: uint32(proof.Count), Siblings: proof.Siblings}, nil
	}
	return nil, nil
}

func loadRootHistory(ctx context.Context, tx pgx.Tx, principal string) ([]*pb.RootHistoryProof, error) {
	rows, err := tx.Query(ctx, `SELECT transcript,old_signature,new_signature FROM root_rotations WHERE principal_id=$1 AND completed_at IS NOT NULL ORDER BY (convert_from(transcript,'UTF8')::jsonb->>'auth_epoch')::bigint LIMIT 17`, principal)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	var proofs []*pb.RootHistoryProof
	for rows.Next() {
		p := new(pb.RootHistoryProof)
		if err = rows.Scan(&p.Transcript, &p.OldSignature, &p.NewSignature); err != nil {
			return nil, databaseError(ctx, err)
		}
		proofs = append(proofs, p)
	}
	return proofs, databaseErrorOrNil(ctx, rows.Err())
}
