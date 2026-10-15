package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func requireHealthyMlsRoster(ctx context.Context, tx pgx.Tx, channel string) error {
	var invalid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mls_members current_member WHERE current_member.channel_id=$1 AND current_member.left_sequence IS NULL AND NOT EXISTS (
      SELECT 1 FROM mls_device_routes route JOIN device_grants g ON g.id=route.current_grant_id JOIN principals p ON p.id=g.principal_id JOIN memberships m ON m.principal_id=p.id
      WHERE route.leaf_grant_id=current_member.grant_id AND g.revoked_at IS NULL AND g.expires_at>now() AND g.auth_epoch=p.auth_epoch AND NOT m.blocked AND 'chat.read'=ANY(g.scopes)`+activeParentCondition+activePairCondition+`))`, channel).Scan(&invalid)
	if err != nil {
		return databaseError(ctx, err)
	}
	if invalid {
		return status.Error(codes.FailedPrecondition, "В канале есть недействующее устройство. Управляющему нужно исключить его из MLS-группы.")
	}
	return nil
}
