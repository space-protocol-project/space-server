package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func contentRequestHash(req *pb.CreateContentRequest) [32]byte {
	if len(req.AttachmentIds) == 0 {
		return sha256.Sum256([]byte(req.Text))
	}
	data, _ := json.Marshal([]any{req.Text, req.AttachmentIds})
	return sha256.Sum256(data)
}
func contentAssets(ctx context.Context, tx pgx.Tx, req *pb.CreateContentRequest) ([]*pb.Asset, error) {
	result := make([]*pb.Asset, 0, len(req.AttachmentIds))
	if err := requireContentAttachments(ctx, tx, req.AttachmentIds); err != nil {
		return nil, err
	}
	if len(req.AttachmentIds) > 5 {
		return nil, status.Error(codes.InvalidArgument, "Не более пяти файлов")
	}
	seen := map[string]bool{}
	for _, id := range req.AttachmentIds {
		if seen[id] {
			return nil, status.Error(codes.InvalidArgument, "Файл указан повторно")
		}
		seen[id] = true
		a, err := readStoredAsset(tx.QueryRow(ctx, `SELECT `+assetColumns+`,received_bytes,ready,content_id,mls_sequence FROM assets WHERE id=$1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "Вложение не найдено")
		}
		if err != nil {
			return nil, databaseError(ctx, err)
		}
		if a.asset.ChannelId != req.ChannelId || a.asset.AuthorId != authn.Actor(ctx) {
			return nil, status.Error(codes.PermissionDenied, "Вложение другого автора или канала")
		}
		if !a.ready || a.contentID != nil {
			return nil, status.Error(codes.FailedPrecondition, "Вложение не готово или уже опубликовано")
		}
		result = append(result, a.asset)
	}
	return result, nil
}
func bindContentAssets(ctx context.Context, tx pgx.Tx, content *pb.Content) error {
	for _, a := range content.Attachments {
		if _, err := tx.Exec(ctx, "UPDATE assets SET content_id=$2 WHERE id=$1", a.Id, content.Id); err != nil {
			return databaseError(ctx, err)
		}
	}
	return nil
}

func insertContentWithAssets(ctx context.Context, tx pgx.Tx, req *pb.CreateContentRequest, content *pb.Content, textHash [32]byte) error {
	attachments, err := contentAssets(ctx, tx, req)
	if err != nil {
		return err
	}
	content.Attachments = attachments
	metadata, err := json.Marshal(attachments)
	if err != nil {
		return databaseError(ctx, err)
	}
	err = tx.QueryRow(ctx, "INSERT INTO contents(channel_id,sequence,id,text,idempotency_key,author_id,request_text_hash,attachments) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at_unix_ms", req.ChannelId, content.Sequence, content.Id, req.Text, req.IdempotencyKey, content.AuthorId, textHash[:], metadata).Scan(&content.CreatedAtUnixMs)
	if err != nil {
		return databaseError(ctx, err)
	}
	if err = bindContentAssets(ctx, tx, content); err != nil {
		return err
	}
	return nil
}

func replayContent(ctx context.Context, previous *pb.Content, previousHash []byte, hash [32]byte, metadata []byte) (*pb.CreateContentResponse, error) {
	if !bytes.Equal(previousHash, hash[:]) {
		return nil, status.Error(codes.AlreadyExists, "Ключ уже использован с другим содержимым")
	}
	if err := json.Unmarshal(metadata, &previous.Attachments); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CreateContentResponse{Content: previous}, nil
}

func requireContentAttachments(ctx context.Context, tx pgx.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return requireAttachmentsEnabled(ctx, tx)
}
