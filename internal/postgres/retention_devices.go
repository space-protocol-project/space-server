package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"time"
)

func listRetentionDevices(ctx context.Context, tx pgx.Tx, principal string) (*pb.ListDevicesResponse, error) {
	// Блокировка смены root: список и доказательства должны принадлежать одной эпохе.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616003)"); err != nil {
		return nil, databaseError(ctx, err)
	}
	rows, err := tx.Query(ctx, `SELECT g.id,g.device_public_key,g.scopes,g.expires_at,g.revoked_at IS NOT NULL,COALESCE(g.parent_grant_id,''),'identity.recover'=ANY(g.scopes),g.auth_epoch,g.signature_kind,g.registration_transcript,g.root_signature,COALESCE(parent.registration_transcript,''::bytea),COALESCE(parent.root_signature,''::bytea),(`+retainableGrant+`) FROM device_grants g JOIN principals p ON p.id=g.principal_id LEFT JOIN device_grants parent ON parent.id=g.parent_grant_id WHERE g.principal_id=$1 ORDER BY (`+retainableGrant+`) DESC,g.expires_at DESC,g.id LIMIT 513`, principal)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	result := new(pb.ListDevicesResponse)
	for rows.Next() {
		d := new(pb.DeviceGrant)
		var expiry time.Time
		if err = rows.Scan(&d.Id, &d.PublicKey, &d.Scopes, &expiry, &d.Revoked, &d.ParentGrantId, &d.Recovery, &d.AuthEpoch, &d.SignatureKind, &d.RegistrationTranscript, &d.Signature, &d.ParentTranscript, &d.ParentSignature, &d.Retainable); err != nil {
			rows.Close()
			return nil, databaseError(ctx, err)
		}
		d.ExpiresAt = expiry.Unix()
		result.Devices = append(result.Devices, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	active := 0
	for _, d := range result.Devices {
		if !d.Retainable {
			continue
		}
		active++
		if d.SignatureKind == "retained" {
			d.RetentionProof, err = loadRetentionProof(ctx, tx, principal, d.Id, d.AuthEpoch)
			if err != nil {
				return nil, err
			}
		}
	}
	if active > 257 {
		return nil, status.Error(codes.ResourceExhausted, "Слишком много действующих устройств для одной смены root")
	}
	return result, nil
}
