package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Store) Update(ctx context.Context, req *pb.UpdateContentRequest) (*pb.UpdateContentResponse, error) {
	content, err := s.changeContent(ctx, req.ChannelId, req.Id, req.ExpectedRevision, req.Text, false)
	return &pb.UpdateContentResponse{Content: content}, err
}

func (s *Store) Delete(ctx context.Context, req *pb.DeleteContentRequest) (*pb.DeleteContentResponse, error) {
	content, err := s.changeContent(ctx, req.ChannelId, req.Id, req.ExpectedRevision, "", true)
	return &pb.DeleteContentResponse{Content: content}, err
}

func loadContent(ctx context.Context, tx pgx.Tx, channel, id string) (*pb.Content, error) {
	content := new(pb.Content)
	err := tx.QueryRow(ctx, `SELECT id,channel_id,text,author_id,sequence,created_at_unix_ms,revision,updated_at_unix_ms,deleted
 FROM contents WHERE channel_id=$1 AND id=$2 FOR UPDATE`, channel, id).Scan(
		&content.Id, &content.ChannelId, &content.Text, &content.AuthorId, &content.Sequence,
		&content.CreatedAtUnixMs, &content.Revision, &content.UpdatedAtUnixMs, &content.Deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "Сообщение не найдено")
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	return content, nil
}

func validateContentAuthor(ctx context.Context, content *pb.Content, revision int64) error {
	actor := authn.Actor(ctx)
	if authn.Token(ctx) == "" || actor != content.AuthorId {
		return status.Error(codes.PermissionDenied, "Изменить сообщение может только автор")
	}
	if content.Revision != revision {
		return status.Error(codes.Aborted, "Сообщение уже изменено. Обновите его и повторите действие")
	}
	if content.Deleted {
		return status.Error(codes.FailedPrecondition, "Сообщение удалено")
	}
	return nil
}

func (s *Store) changeContent(ctx context.Context, channel, id string, revision int64, text string, deleted bool) (*pb.Content, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err = s.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	if _, err = channelAccess(ctx, tx, channel, "write", true); err != nil {
		return nil, err
	}
	content, err := loadContent(ctx, tx, channel, id)
	if err != nil {
		return nil, err
	}
	if err = validateContentAuthor(ctx, content, revision); err != nil {
		return nil, err
	}
	content.Text, content.Deleted = text, deleted
	content.Revision++
	content.UpdatedAtUnixMs = time.Now().UnixMilli()
	if err = saveContentChange(ctx, tx, content); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return content, nil
}

func saveContentChange(ctx context.Context, tx pgx.Tx, content *pb.Content) error {
	_, err := tx.Exec(ctx, `UPDATE contents SET text=$3,revision=$4,updated_at_unix_ms=$5,deleted=$6
 WHERE channel_id=$1 AND id=$2`, content.ChannelId, content.Id, content.Text, content.Revision, content.UpdatedAtUnixMs, content.Deleted)
	if err != nil {
		return databaseError(ctx, err)
	}
	kind := "content.updated"
	if content.Deleted {
		kind = "content.deleted"
	}
	_, err = tx.Exec(ctx, `INSERT INTO events(channel_id,sequence,content_sequence,cursor,type)
 SELECT id,next_event_sequence,$2,'event-' || next_event_sequence,$3 FROM channels WHERE id=$1`, content.ChannelId, content.Sequence, kind)
	if err != nil {
		return databaseError(ctx, err)
	}
	_, err = tx.Exec(ctx, "UPDATE channels SET next_event_sequence=next_event_sequence+1 WHERE id=$1", content.ChannelId)
	if err != nil {
		return databaseError(ctx, err)
	}
	return nil
}
