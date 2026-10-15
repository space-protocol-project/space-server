package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mlsScope struct {
	ServerID    string   `json:"server_id"`
	PrincipalID string   `json:"principal_id"`
	GrantID     string   `json:"grant_id"`
	PublicKey   [32]byte `json:"public_key"`
}
type mlsBinding struct {
	Device    mlsScope `json:"device"`
	PublicKey [32]byte `json:"mls_public_key"`
	Signature [64]byte `json:"signature"`
}

func sessionMlsScope(ctx context.Context, tx pgx.Tx) (mlsScope, error) {
	var result mlsScope
	var key []byte
	hash := sha256.Sum256([]byte(authn.Token(ctx)))
	err := tx.QueryRow(ctx, `SELECT COALESCE(r.leaf_grant_id,g.id),g.principal_id,COALESCE(original.device_public_key,g.device_public_key),i.server_id FROM auth_sessions s JOIN device_grants g ON g.id=s.grant_id LEFT JOIN mls_device_routes r ON r.current_grant_id=g.id LEFT JOIN device_grants original ON original.id=r.leaf_grant_id CROSS JOIN server_state i WHERE s.token_hash=$1`, hash[:]).Scan(&result.GrantID, &result.PrincipalID, &key, &result.ServerID)
	if err != nil {
		return result, databaseError(ctx, err)
	}
	if len(key) != 32 {
		return result, denied()
	}
	copy(result.PublicKey[:], key)
	return result, nil
}

func validateMlsBinding(raw []byte, expected mlsScope) error {
	if len(raw) == 0 || len(raw) > 8192 {
		return status.Error(codes.InvalidArgument, "Некорректная привязка MLS")
	}
	var binding mlsBinding
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return status.Error(codes.InvalidArgument, "Некорректная привязка MLS")
	}
	if decoder.Decode(new(any)) != io.EOF || binding.Device != expected {
		return forbidden()
	}
	signing, err := json.Marshal([]any{"space/mls/binding/v1", binding.Device, binding.PublicKey})
	if err != nil || !ed25519.Verify(expected.PublicKey[:], signing, binding.Signature[:]) {
		return forbidden()
	}
	return nil
}

func (s *Store) RegisterMlsDevice(ctx context.Context, req *pb.RegisterMlsDeviceRequest) (*pb.RegisterMlsDeviceResponse, error) {
	if len(req.KeyPackage) == 0 || len(req.KeyPackage) > 65536 {
		return nil, status.Error(codes.InvalidArgument, "Некорректный KeyPackage")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err = s.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	member, err := membership(ctx, tx, authn.Actor(ctx), true)
	if err != nil {
		return nil, err
	}
	if member.Blocked {
		return nil, forbidden()
	}
	scope, err := sessionMlsScope(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err = validateMlsBinding(req.Binding, scope); err != nil {
		return nil, err
	}
	var previous []byte
	err = tx.QueryRow(ctx, "SELECT binding FROM mls_devices WHERE grant_id=$1 FOR UPDATE", scope.GrantID).Scan(&previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, databaseError(ctx, err)
	}
	if len(previous) > 0 && !bytes.Equal(previous, req.Binding) {
		return nil, status.Error(codes.AlreadyExists, "MLS-ключ этого устройства уже закреплён")
	}
	_, err = tx.Exec(ctx, `INSERT INTO mls_devices(grant_id,binding,key_package) VALUES($1,$2,$3) ON CONFLICT(grant_id) DO UPDATE SET key_package=EXCLUDED.key_package,consumed=CASE WHEN mls_devices.key_package=EXCLUDED.key_package THEN mls_devices.consumed ELSE false END`, scope.GrantID, req.Binding, req.KeyPackage)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = ensureMlsRoute(ctx, tx, scope.GrantID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.RegisterMlsDeviceResponse{Device: &pb.MlsDevice{GrantId: scope.GrantID, PrincipalId: scope.PrincipalID, Binding: req.Binding, KeyPackage: req.KeyPackage}}, nil
}

func (s *Store) ListMlsDevices(ctx context.Context, req *pb.ListMlsDevicesRequest) (*pb.ListMlsDevicesResponse, error) {
	tx, err := s.mlsTransaction(ctx, req.ChannelId, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT d.grant_id,g.principal_id,d.binding,CASE WHEN d.consumed THEN ''::bytea ELSE d.key_package END FROM mls_devices d JOIN mls_device_routes route ON route.leaf_grant_id=d.grant_id JOIN device_grants g ON g.id=route.current_grant_id JOIN principals p ON p.id=g.principal_id JOIN memberships m ON m.principal_id=p.id WHERE d.grant_id COLLATE "C">$1 AND g.revoked_at IS NULL AND g.expires_at>now() AND g.auth_epoch=p.auth_epoch AND NOT m.blocked`+activeParentCondition+activePairCondition+` ORDER BY d.grant_id COLLATE "C" LIMIT 101`, req.After)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := new(pb.ListMlsDevicesResponse)
	for rows.Next() {
		d := new(pb.MlsDevice)
		if err = rows.Scan(&d.GrantId, &d.PrincipalId, &d.Binding, &d.KeyPackage); err != nil {
			return nil, databaseError(ctx, err)
		}
		result.Devices = append(result.Devices, d)
	}
	if err = rows.Err(); err != nil {
		return nil, databaseError(ctx, err)
	}
	if len(result.Devices) > 100 {
		result.Devices = result.Devices[:100]
		result.NextCursor = result.Devices[99].GrantId
	}
	return result, nil
}

func ensureMlsRoute(ctx context.Context, tx pgx.Tx, leaf string) error {
	_, err := tx.Exec(ctx, "INSERT INTO mls_device_routes(leaf_grant_id,current_grant_id) VALUES($1,$1) ON CONFLICT(leaf_grant_id) DO NOTHING", leaf)
	return databaseErrorOrNil(ctx, err)
}
