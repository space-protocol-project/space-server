package postgres

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (s *Store) ListMlsEnvelopes(ctx context.Context, req *pb.ListMlsEnvelopesRequest) (*pb.ListMlsEnvelopesResponse, error) {
	if req.AfterSequence < 0 {
		return nil, status.Error(codes.InvalidArgument, "Некорректный курсор")
	}
	tx, err := s.mlsTransaction(ctx, req.ChannelId, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	scope, err := sessionMlsScope(ctx, tx)
	if err != nil {
		return nil, err
	}
	joined, err := activeMlsMember(ctx, tx, req.ChannelId, scope.GrantID)
	if err != nil {
		return nil, err
	}
	var sequence int64
	if err = tx.QueryRow(ctx, "SELECT sequence FROM mls_channels WHERE channel_id=$1", req.ChannelId).Scan(&sequence); err != nil {
		return nil, databaseError(ctx, err)
	}
	if req.AfterSequence > sequence {
		return nil, status.Error(codes.FailedPrecondition, "Архив сервера старше состояния MLS на устройстве. Сохраните локальную историю; откат состояния устройства запрещён.")
	}
	rows, err := tx.Query(ctx, "SELECT envelope FROM mls_envelopes WHERE channel_id=$1 AND sequence>$2 AND sequence>=$3 ORDER BY sequence LIMIT 20", req.ChannelId, req.AfterSequence, joined)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := &pb.ListMlsEnvelopesResponse{NextSequence: req.AfterSequence}
	pageSize := 0
	for rows.Next() {
		var encoded []byte
		if err = rows.Scan(&encoded); err != nil {
			return nil, databaseError(ctx, err)
		}
		if pageSize+len(encoded) > 2*1024*1024 && len(result.Envelopes) > 0 {
			break
		}
		pageSize += len(encoded)
		envelope := new(pb.MlsEnvelope)
		if proto.Unmarshal(encoded, envelope) != nil {
			return nil, status.Error(codes.Internal, "Повреждён MLS-пакет")
		}
		if envelope.AddedGrantId != scope.GrantID {
			envelope.Welcome = nil
		}
		result.Envelopes = append(result.Envelopes, envelope)
		result.NextSequence = envelope.Sequence
	}
	return result, databaseErrorOrNil(ctx, rows.Err())
}
