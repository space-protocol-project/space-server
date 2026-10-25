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

type contentMutation struct {
	mentions                            []*pb.CommunityMention
	kind, channel, id, key, text, emoji string
	revision, applied                   int64
	active                              bool
}

func (m *contentMutation) hash() []byte {
	parts := []any{m.kind, m.channel, m.id, m.revision, m.text, m.emoji, m.active}
	if len(m.mentions) > 0 {
		parts = append(parts, m.mentions)
	}
	encoded, _ := json.Marshal(parts)
	digest := sha256.Sum256(encoded)
	return digest[:]
}

func (m *contentMutation) replay(ctx context.Context, tx pgx.Tx, content *pb.Content) (bool, error) {
	if m.key == "" {
		return false, nil
	}
	if len(m.key) > 128 || m.revision < 1 {
		return false, status.Error(codes.InvalidArgument, "Недопустимый ключ изменения")
	}
	var digest []byte
	var id string
	err := tx.QueryRow(ctx, `SELECT request_hash,content_id,applied_revision FROM content_mutations
        WHERE channel_id=$1 AND author_id=$2 AND idempotency_key=$3`, m.channel, authn.Actor(ctx), m.key).
		Scan(&digest, &id, &m.applied)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, databaseError(ctx, err)
	}
	if id != content.Id || !bytes.Equal(digest, m.hash()) {
		return false, status.Error(codes.AlreadyExists, "Ключ изменения занят другим запросом")
	}
	return true, nil
}

func (m *contentMutation) save(ctx context.Context, tx pgx.Tx, content *pb.Content) error {
	m.applied = content.Revision
	if m.key == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO content_mutations
        (channel_id,author_id,idempotency_key,content_id,request_hash,applied_revision)
        VALUES($1,$2,$3,$4,$5,$6)`, m.channel, authn.Actor(ctx), m.key, m.id, m.hash(), m.applied)
	return databaseErrorOrNil(ctx, err)
}
