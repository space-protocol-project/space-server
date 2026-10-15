package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validateMlsTransfer(req *pb.PublishMlsRequest) error {
	if req.Kind == "application" && req.NewManagerGrantId == "" {
		return nil
	}
	if req.Kind == "transfer" && len(req.NewManagerGrantId) > 0 && len(req.NewManagerGrantId) <= 128 && len(req.AttachmentIds) == 0 {
		return nil
	}
	return status.Error(codes.InvalidArgument, "Некорректная передача управления MLS")
}

func requireMlsManager(ctx context.Context, tx pgx.Tx, req *pb.PublishMlsRequest, leaf string) error {
	if req.Kind == "application" {
		return nil
	}
	var manager string
	if err := tx.QueryRow(ctx, "SELECT manager_grant_id FROM mls_channels WHERE channel_id=$1", req.ChannelId).Scan(&manager); err != nil {
		return databaseError(ctx, err)
	}
	if leaf != manager || req.RemoveGrantId == manager || req.NewManagerGrantId == manager {
		return forbidden()
	}
	return nil
}

func transferMlsManager(ctx context.Context, tx pgx.Tx, req *pb.PublishMlsRequest) error {
	if req.Kind != "transfer" {
		return nil
	}
	if err := requireHealthyMlsRoster(ctx, tx, req.ChannelId); err != nil {
		return err
	}
	if _, err := activeMlsMember(ctx, tx, req.ChannelId, req.NewManagerGrantId); err != nil {
		return err
	}
	if err := requireWritableMlsDevice(ctx, tx, req.ChannelId, req.NewManagerGrantId); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE mls_channels SET manager_grant_id=$2 WHERE channel_id=$1", req.ChannelId, req.NewManagerGrantId)
	return databaseErrorOrNil(ctx, err)
}

func requireWritableMlsDevice(ctx context.Context, tx pgx.Tx, channel, leaf string) error {
	var principal string
	err := tx.QueryRow(ctx, `SELECT g.principal_id FROM mls_device_routes r JOIN device_grants g ON g.id=r.current_grant_id WHERE r.leaf_grant_id=$1 AND 'chat.write'=ANY(g.scopes)`, leaf).Scan(&principal)
	if err == pgx.ErrNoRows {
		return status.Error(codes.FailedPrecondition, "Устройству необходим доступ к записи в канал")
	}
	if err != nil {
		return databaseError(ctx, err)
	}
	member, err := membership(ctx, tx, principal, false)
	if err != nil {
		return err
	}
	permissions, err := effectivePermissions(ctx, tx, channel, member)
	if err != nil {
		return err
	}
	if !permissions.Write {
		return forbidden()
	}
	return nil
}
