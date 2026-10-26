package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func favoriteLock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,73616017))", authn.Actor(ctx))
	return databaseErrorOrNil(ctx, err)
}

func favoriteReplay(ctx context.Context, tx pgx.Tx, req *pb.SetCommunityFavoriteRequest, hash []byte) (int64, bool, error) {
	var previous []byte
	var applied int64
	err := tx.QueryRow(ctx, `SELECT request_hash,applied_revision FROM community_favorite_requests WHERE principal_id=$1 AND channel_id=$2 AND idempotency_key=$3 AND created_at>=clock_timestamp()-interval '14 days'`, authn.Actor(ctx), req.ChannelId, req.IdempotencyKey).Scan(&previous, &applied)
	if err == pgx.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, databaseError(ctx, err)
	}
	if !bytes.Equal(previous, hash) {
		return 0, false, forumKeyConflict()
	}
	return applied, true, nil
}

func favoriteBudget(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `DELETE FROM community_favorite_requests WHERE principal_id=$1 AND created_at<clock_timestamp()-interval '14 days'`, authn.Actor(ctx)); err != nil {
		return databaseError(ctx, err)
	}
	var count int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM community_favorite_requests WHERE principal_id=$1 AND created_at>=clock_timestamp()-interval '1 minute'`, authn.Actor(ctx)).Scan(&count)
	if err != nil {
		return databaseError(ctx, err)
	}
	if count >= 60 {
		return status.Error(codes.ResourceExhausted, "Не более 60 новых операций избранного в минуту")
	}
	return nil
}

func applyCommunityFavorite(ctx context.Context, tx pgx.Tx, req *pb.SetCommunityFavoriteRequest, current *pb.CommunityFavoriteState) error {
	if current.Revision != req.ExpectedRevision {
		return status.Error(codes.Aborted, "Избранное изменилось; обновите состояние")
	}
	if current.Saved == req.Saved {
		return nil
	}
	if req.Saved {
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM community_favorites WHERE principal_id=$1 AND saved", authn.Actor(ctx)).Scan(&count); err != nil {
			return databaseError(ctx, err)
		}
		if count >= 5000 {
			return status.Error(codes.ResourceExhausted, "Не более 5000 сохранённых объектов")
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO community_favorites(principal_id,channel_id,kind,target_id,saved,revision,saved_at)
 VALUES($1,$2,$3,$4,$5,1,(extract(epoch FROM clock_timestamp())*1000000)::bigint)
 ON CONFLICT(principal_id,channel_id,kind,target_id) DO UPDATE SET saved=$5,revision=community_favorites.revision+1,saved_at=excluded.saved_at`, authn.Actor(ctx), req.ChannelId, int32(req.Content.Kind), req.Content.Id, req.Saved)
	return databaseErrorOrNil(ctx, err)
}

func favoriteTarget(ctx context.Context, tx pgx.Tx, c *pb.Channel, req *pb.SetCommunityFavoriteRequest) error {
	_, err := communityContentTarget(ctx, tx, c, req.Content)
	// Снять свою отметку можно после скрытия объекта, не раскрывая его текст.
	if !req.Saved && status.Code(err) == codes.NotFound {
		return nil
	}
	return err
}

func (s *Store) SetCommunityFavorite(ctx context.Context, req *pb.SetCommunityFavoriteRequest) (*pb.SetCommunityFavoriteResponse, error) {
	if !validCommunityReference(req.Content) || req.ExpectedRevision < 0 || !validForumText(req.IdempotencyKey, 128) {
		return nil, status.Error(codes.InvalidArgument, "Некорректный объект, ревизия или ключ")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, c, err := s.communityContentTransaction(ctx, req.ChannelId, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = favoriteTarget(ctx, tx, c, req); err != nil {
		return nil, err
	}
	if err = favoriteLock(ctx, tx); err != nil {
		return nil, err
	}
	encoded, _ := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	hash := sha256.Sum256(encoded)
	applied, replayed, err := favoriteReplay(ctx, tx, req, hash[:])
	if err != nil {
		return nil, err
	}
	current, err := favoriteState(ctx, tx, c.Id, req.Content)
	if err != nil {
		return nil, err
	}
	if replayed {
		return &pb.SetCommunityFavoriteResponse{State: current, IdempotencyKey: req.IdempotencyKey, AppliedRevision: applied}, nil
	}
	if err = favoriteBudget(ctx, tx); err != nil {
		return nil, err
	}
	if err = applyCommunityFavorite(ctx, tx, req, current); err != nil {
		return nil, err
	}
	current, err = favoriteState(ctx, tx, c.Id, req.Content)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO community_favorite_requests(principal_id,channel_id,idempotency_key,request_hash,applied_revision) VALUES($1,$2,$3,$4,$5)`, authn.Actor(ctx), req.ChannelId, req.IdempotencyKey, hash[:], current.Revision)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.SetCommunityFavoriteResponse{State: current, IdempotencyKey: req.IdempotencyKey, AppliedRevision: current.Revision}, nil
}
