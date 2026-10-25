package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Store) Update(ctx context.Context, req *pb.UpdateContentRequest) (*pb.UpdateContentResponse, error) {
	m := &contentMutation{kind: "update", mentions: req.Mentions, channel: req.ChannelId, id: req.Id, revision: req.ExpectedRevision, text: req.Text, key: req.IdempotencyKey}
	content, err := s.changeContent(ctx, m, false)
	return &pb.UpdateContentResponse{Content: content, IdempotencyKey: m.key, AppliedRevision: m.applied}, err
}

func (s *Store) Delete(ctx context.Context, req *pb.DeleteContentRequest) (*pb.DeleteContentResponse, error) {
	m := &contentMutation{kind: "delete", channel: req.ChannelId, id: req.Id, revision: req.ExpectedRevision, key: req.IdempotencyKey}
	content, err := s.changeContent(ctx, m, true)
	return &pb.DeleteContentResponse{Content: content, IdempotencyKey: m.key, AppliedRevision: m.applied}, err
}

func loadContent(ctx context.Context, tx pgx.Tx, channel, id string) (*pb.Content, error) {
	content := new(pb.Content)
	var metadata, reactionData []byte
	err := tx.QueryRow(ctx, `SELECT id,channel_id,text,author_id,sequence,created_at_unix_ms,revision,updated_at_unix_ms,deleted,attachments,reactions,COALESCE(reply_to_id,'')
 FROM contents WHERE channel_id=$1 AND id=$2 FOR UPDATE`, channel, id).Scan(
		&content.Id, &content.ChannelId, &content.Text, &content.AuthorId, &content.Sequence,
		&content.CreatedAtUnixMs, &content.Revision, &content.UpdatedAtUnixMs, &content.Deleted, &metadata, &reactionData, &content.ReplyToId)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "Сообщение не найдено")
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = json.Unmarshal(metadata, &content.Attachments); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = json.Unmarshal(reactionData, &content.Reactions); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = populateContentNames(ctx, tx, []*pb.Content{content}); err != nil {
		return nil, err
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

func (s *Store) changeContent(ctx context.Context, m *contentMutation, deleted bool) (*pb.Content, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err = s.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	if err = requireOpenChannelAccess(ctx, tx, m.channel, "write"); err != nil {
		return nil, err
	}
	content, err := loadContent(ctx, tx, m.channel, m.id)
	if err != nil {
		return nil, err
	}
	if found, replayErr := m.replay(ctx, tx, content); replayErr != nil || found {
		return content, replayErr
	}
	if err = validateContentAuthor(ctx, content, m.revision); err != nil {
		return nil, err
	}
	if !deleted && strings.TrimSpace(m.text) == "" && len(content.Attachments) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Сообщение должно содержать текст или файл")
	}
	if !deleted {
		if _, err = validateCommunityMentions(ctx, tx, m.channel, m.text, m.mentions); err != nil {
			return nil, err
		}
	}
	content.Mentions = m.mentions
	if deleted {
		if _, err = tx.Exec(ctx, "DELETE FROM assets WHERE channel_id=$1 AND content_id=$2", m.channel, m.id); err != nil {
			return nil, databaseError(ctx, err)
		}
		content.Attachments = nil
		content.Reactions = nil
	}
	content.Text, content.Deleted = m.text, deleted
	content.Revision++
	content.UpdatedAtUnixMs = time.Now().UnixMilli()
	if err = saveContentChange(ctx, tx, content); err != nil {
		return nil, err
	}
	if err = m.save(ctx, tx, content); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return content, nil
}

func saveContentChange(ctx context.Context, tx pgx.Tx, content *pb.Content) error {
	reactions, err := json.Marshal(content.Reactions)
	if err != nil {
		return databaseError(ctx, err)
	}
	if content.Reactions == nil {
		reactions = []byte("[]")
	}
	_, err = tx.Exec(ctx, `UPDATE contents SET text=$3,revision=$4,updated_at_unix_ms=$5,deleted=$6,attachments=CASE WHEN $6 THEN '[]'::jsonb ELSE attachments END,reactions=$7,mentions=$8
 WHERE channel_id=$1 AND id=$2`, content.ChannelId, content.Id, content.Text, content.Revision, content.UpdatedAtUnixMs, content.Deleted, reactions, mentionJSON(content.Mentions))
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
	return populateContentNames(ctx, tx, []*pb.Content{content})
}
