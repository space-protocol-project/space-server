package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Store) mlsTransaction(ctx context.Context, channel, action string) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = s.lockSession(ctx, tx); err == nil {
		_, err = channelAccess(ctx, tx, channel, action, true)
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func initializeMlsChannel(ctx context.Context, tx pgx.Tx, req *pb.CreateChannelRequest) error {
	if len(req.MlsGroupId) == 0 {
		return nil
	}
	scope, err := sessionMlsScope(ctx, tx)
	if err != nil {
		return err
	}
	var registered bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mls_devices WHERE grant_id=$1)", scope.GrantID).Scan(&registered); err != nil {
		return databaseError(ctx, err)
	}
	if !registered {
		return status.Error(codes.FailedPrecondition, "Сначала зарегистрируйте MLS-ключ устройства")
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mls_channels(channel_id,manager_grant_id) VALUES($1,$2)", req.ChannelId, scope.GrantID); err != nil {
		return databaseError(ctx, err)
	}
	_, err = tx.Exec(ctx, "INSERT INTO mls_members(channel_id,grant_id,joined_sequence) VALUES($1,$2,0)", req.ChannelId, scope.GrantID)
	return databaseErrorOrNil(ctx, err)
}

func databaseErrorOrNil(ctx context.Context, err error) error {
	if err != nil {
		return databaseError(ctx, err)
	}
	return nil
}

func validateMlsChannelRequest(req *pb.CreateChannelRequest) error {
	if len(req.MlsGroupId) > 0 && (len(req.MlsGroupId) < 16 || len(req.MlsGroupId) > 256 || req.PublicPreview) {
		return status.Error(codes.InvalidArgument, "Некорректный закрытый канал")
	}
	return nil
}

func mlsGroupOrNull(group []byte) any {
	if len(group) == 0 {
		return nil
	}
	return group
}

func requireOpenChannel(ctx context.Context, tx pgx.Tx, channel string) error {
	var encrypted bool
	var kind string
	if err := tx.QueryRow(ctx, "SELECT mls_group_id IS NOT NULL,view_type FROM channels WHERE id=$1", channel).Scan(&encrypted, &kind); err != nil {
		return databaseError(ctx, err)
	}
	if kind != "chat" {
		return status.Error(codes.FailedPrecondition, "Для этого раздела требуется отдельный API")
	}
	if encrypted {
		return status.Error(codes.FailedPrecondition, "Для закрытого канала требуется MLS")
	}
	return nil
}

func requireOpenChannelAccess(ctx context.Context, tx pgx.Tx, channel, action string) error {
	if _, err := channelAccess(ctx, tx, channel, action, true); err != nil {
		return err
	}
	return requireOpenChannel(ctx, tx, channel)
}

func activeMlsMember(ctx context.Context, tx pgx.Tx, channel, grant string) (int64, error) {
	var joined int64
	err := tx.QueryRow(ctx, "SELECT joined_sequence FROM mls_members WHERE channel_id=$1 AND grant_id=$2 AND left_sequence IS NULL", channel, grant).Scan(&joined)
	if err == pgx.ErrNoRows {
		return 0, forbidden()
	}
	if err != nil {
		return 0, databaseError(ctx, err)
	}
	return joined, nil
}

func mlsRoster(ctx context.Context, tx pgx.Tx, channel string) ([]*pb.MlsDevice, error) {
	rows, err := tx.Query(ctx, `SELECT d.grant_id,g.principal_id,d.binding FROM mls_members m JOIN mls_devices d ON d.grant_id=m.grant_id JOIN device_grants g ON g.id=d.grant_id WHERE m.channel_id=$1 AND m.left_sequence IS NULL ORDER BY d.grant_id`, channel)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := []*pb.MlsDevice{}
	for rows.Next() {
		d := new(pb.MlsDevice)
		if err = rows.Scan(&d.GrantId, &d.PrincipalId, &d.Binding); err != nil {
			return nil, databaseError(ctx, err)
		}
		result = append(result, d)
	}
	return result, databaseErrorOrNil(ctx, rows.Err())
}

func (s *Store) GetMlsChannel(ctx context.Context, req *pb.GetMlsChannelRequest) (*pb.GetMlsChannelResponse, error) {
	tx, err := s.mlsTransaction(ctx, req.ChannelId, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	result := new(pb.GetMlsChannelResponse)
	err = tx.QueryRow(ctx, `SELECT c.mls_group_id,m.epoch FROM channels c JOIN mls_channels m ON m.channel_id=c.id WHERE c.id=$1`, req.ChannelId).Scan(&result.GroupId, &result.Epoch)
	if err == pgx.ErrNoRows {
		return nil, status.Error(codes.FailedPrecondition, "Это открытый канал")
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	result.Devices, err = mlsRoster(ctx, tx, req.ChannelId)
	return result, err
}
