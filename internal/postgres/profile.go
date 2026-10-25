package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

func profileNameKey(nickname string) string {
	return cases.Fold().String(norm.NFKC.String(strings.TrimSpace(nickname)))
}

func requireUniqueProfileName(ctx context.Context, tx pgx.Tx, actor, nickname string) error {
	if nickname == "" {
		return nil
	}
	rows, err := tx.Query(ctx, "SELECT nickname FROM memberships WHERE principal_id<>$1 AND nickname<>''", actor)
	if err != nil {
		return databaseError(ctx, err)
	}
	defer rows.Close()
	key := profileNameKey(nickname)
	for rows.Next() {
		var existing string
		if err = rows.Scan(&existing); err != nil {
			return databaseError(ctx, err)
		}
		if profileNameKey(existing) == key {
			return status.Error(codes.AlreadyExists, "Это имя уже занято")
		}
	}
	if err = rows.Err(); err != nil {
		return databaseError(ctx, err)
	}
	return nil
}
