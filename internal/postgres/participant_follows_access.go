package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

func validFollowID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range []byte(id) {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func followLimit(limit int32) (int, error) {
	if limit < 0 || limit > 50 {
		return 0, status.Error(codes.InvalidArgument, "Нужна страница до 50 записей")
	}
	if limit == 0 {
		return 20, nil
	}
	return int(limit), nil
}

func followRevision(ctx context.Context, tx pgx.Tx) (int64, error) {
	var revision int64
	err := tx.QueryRow(ctx, "SELECT revision FROM participant_follow_versions WHERE principal_id=$1", authn.Actor(ctx)).Scan(&revision)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	return revision, databaseErrorOrNil(ctx, err)
}

func (s *Store) followTransaction(ctx context.Context) (pgx.Tx, []string, int64, error) {
	tx, channels, err := s.spaceSearchTransaction(ctx)
	if err != nil {
		return nil, nil, 0, err
	}
	_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,73616018))", authn.Actor(ctx))
	var revision int64
	if err == nil {
		revision, err = followRevision(ctx, tx)
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, nil, 0, databaseErrorOrNil(ctx, err)
	}
	return tx, channels.feed, revision, nil
}

func lockFollowMembers(ctx context.Context, tx pgx.Tx, ids []string) error {
	rows, err := tx.Query(ctx, "SELECT principal_id FROM memberships WHERE principal_id=ANY($1) ORDER BY principal_id COLLATE \"C\" FOR SHARE", ids)
	if err != nil {
		return databaseError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return databaseError(ctx, err)
		}
	}
	return databaseErrorOrNil(ctx, rows.Err())
}

const followVisibleAuthor = `EXISTS(SELECT 1 FROM feed_posts p WHERE p.channel_id=shared.id AND p.author_id=m.principal_id AND NOT p.hidden
 UNION ALL SELECT 1 FROM feed_comments c JOIN feed_posts p ON p.id=c.post_id
 WHERE p.channel_id=shared.id AND c.author_id=m.principal_id AND NOT c.hidden AND NOT p.hidden)`

func visibleFollowNames(ctx context.Context, tx pgx.Tx, channels, ids []string) (map[string]string, error) {
	if err := lockFollowMembers(ctx, tx, ids); err != nil {
		return nil, err
	}
	// Политика видимости профиля совпадает с ListParticipantProfiles.
	query := `SELECT DISTINCT visible.principal_id,visible.nickname FROM
 (SELECT id FROM channels WHERE id=ANY($1::text[])) shared CROSS JOIN LATERAL
 (SELECT m.principal_id,m.nickname` + strings.ReplaceAll(mentionMembers, "$1", "shared.id") +
		` AND m.principal_id=ANY($2::text[]) AND ` + followVisibleAuthor + `) visible`
	rows, err := tx.Query(ctx, query, channels, ids)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			return nil, databaseError(ctx, err)
		}
		names[id] = name
	}
	return names, databaseErrorOrNil(ctx, rows.Err())
}

func followState(ctx context.Context, tx pgx.Tx, id string) (*pb.ParticipantFollowState, error) {
	out := &pb.ParticipantFollowState{PrincipalId: id}
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM participant_follows WHERE principal_id=$1 AND target_id=$2)", authn.Actor(ctx), id).Scan(&out.Following)
	return out, databaseErrorOrNil(ctx, err)
}

func followChannel(channels []string, id string) error {
	for _, channel := range channels {
		if channel == id {
			return nil
		}
	}
	return status.Error(codes.NotFound, "Раздел недоступен")
}
