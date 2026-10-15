package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (s *Store) UpdateProfile(ctx context.Context, req *pb.UpdateProfileRequest) (*pb.UpdateProfileResponse, error) {
	nickname := strings.TrimSpace(req.Nickname)
	if !utf8.ValidString(nickname) || len(nickname) > 160 || utf8.RuneCountInString(nickname) > 40 || strings.IndexFunc(nickname, unicode.IsControl) >= 0 {
		return nil, status.Error(codes.InvalidArgument, "Имя должно содержать до 40 символов без переносов строк")
	}
	if authn.Token(ctx) == "" {
		return nil, denied()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err = s.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	member, err := membership(ctx, tx, authn.Actor(ctx), false)
	if err != nil {
		return nil, err
	}
	if member.Blocked {
		return nil, forbidden()
	}
	if err = requireUniqueNickname(ctx, tx, member.PrincipalId, nickname); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, "UPDATE memberships SET nickname=$2,revision=revision+1 WHERE principal_id=$1 AND NOT blocked RETURNING revision", member.PrincipalId, nickname).Scan(&member.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, forbidden()
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	member.Nickname = nickname
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.UpdateProfileResponse{Member: member}, nil
}

func nicknameKey(nickname string) string {
	return cases.Fold().String(norm.NFKC.String(strings.TrimSpace(nickname)))
}

func requireUniqueNickname(ctx context.Context, tx pgx.Tx, actor, nickname string) error {
	// Все изменения ников сериализованы до конца транзакции.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616015)"); err != nil {
		return databaseError(ctx, err)
	}
	if nickname == "" {
		return nil
	}
	rows, err := tx.Query(ctx, "SELECT nickname FROM memberships WHERE principal_id<>$1 AND nickname<>''", actor)
	if err != nil {
		return databaseError(ctx, err)
	}
	defer rows.Close()
	key := nicknameKey(nickname)
	for rows.Next() {
		var existing string
		if err = rows.Scan(&existing); err != nil {
			return databaseError(ctx, err)
		}
		if nicknameKey(existing) == key {
			return status.Error(codes.AlreadyExists, "Это имя уже занято")
		}
	}
	if err = rows.Err(); err != nil {
		return databaseError(ctx, err)
	}
	return nil
}
