package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (s *Store) UpdateProfile(ctx context.Context, req *pb.UpdateProfileRequest) (*pb.UpdateProfileResponse, error) {
	nickname := strings.TrimSpace(req.Nickname)
	if !utf8.ValidString(nickname) || len(nickname) > 160 || utf8.RuneCountInString(nickname) > 40 || strings.IndexFunc(nickname, unicode.IsControl) >= 0 {
		return nil, status.Error(codes.InvalidArgument, "Ник должен содержать до 40 символов без переносов строк")
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
