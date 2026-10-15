package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Store) CompleteUpload(ctx context.Context, req *pb.CompleteUploadRequest) (*pb.CompleteUploadResponse, error) {
	tx, a, err := s.ownedAssetTransaction(ctx, req.AssetId)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if a.ready {
		return &pb.CompleteUploadResponse{Asset: a.asset}, nil
	}
	if a.received != a.asset.ByteSize {
		return nil, status.Error(codes.FailedPrecondition, "Файл загружен не полностью")
	}
	mediaType, err := verifyAssetChunks(ctx, tx, a.asset)
	if err != nil {
		return nil, err
	}
	a.asset.MediaType = mediaType
	_, err = tx.Exec(ctx, "UPDATE assets SET ready=true,media_type=$2 WHERE id=$1", a.asset.Id, mediaType)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CompleteUploadResponse{Asset: a.asset}, nil
}

func verifyAssetChunks(ctx context.Context, tx pgx.Tx, a *pb.Asset) (string, error) {
	rows, err := tx.Query(ctx, "SELECT byte_offset,data FROM asset_chunks WHERE asset_id=$1 ORDER BY byte_offset", a.Id)
	if err != nil {
		return "", databaseError(ctx, err)
	}
	defer rows.Close()
	digest := sha256.New()
	var received int64
	var header []byte
	for rows.Next() {
		var offset int64
		var data []byte
		if err = rows.Scan(&offset, &data); err != nil {
			return "", databaseError(ctx, err)
		}
		if offset != received {
			return "", status.Error(codes.DataLoss, "Нарушен порядок частей файла")
		}
		if len(header) == 0 {
			header = append(header, data[:min(512, len(data))]...)
		}
		digest.Write(data)
		received += int64(len(data))
	}
	if err = rows.Err(); err != nil {
		return "", databaseError(ctx, err)
	}
	if received != a.ByteSize || !bytes.Equal(digest.Sum(nil), a.Sha256) {
		return "", status.Error(codes.DataLoss, "Контрольная сумма файла не совпадает")
	}
	return http.DetectContentType(header), nil
}
