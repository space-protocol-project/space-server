package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/reactions"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func validateReactionChange(req *pb.SetCommunityReactionRequest) error {
	if !validReactionReference(req.Content) || !reactions.Valid(req.Emoji) || req.ExpectedRevision < 0 || !validForumText(req.IdempotencyKey, 128) {
		return status.Error(codes.InvalidArgument, "Некорректная реакция, ревизия или ключ запроса")
	}
	return nil
}

func reactionReplay(ctx context.Context, tx pgx.Tx, req *pb.SetCommunityReactionRequest, hash []byte) (int64, bool, error) {
	var previous []byte
	var applied int64
	err := tx.QueryRow(ctx, `SELECT request_hash,applied_revision FROM community_reaction_requests
 WHERE principal_id=$1 AND channel_id=$2 AND idempotency_key=$3`, authn.Actor(ctx), req.ChannelId, req.IdempotencyKey).Scan(&previous, &applied)
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

func reactionBudget(ctx context.Context, tx pgx.Tx) error {
	// Один бюджет principal для всех разделов и устройств.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,73616016))", authn.Actor(ctx)); err != nil {
		return databaseError(ctx, err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM community_reaction_requests WHERE principal_id=$1 AND created_at<clock_timestamp()-interval '14 days'", authn.Actor(ctx)); err != nil {
		return databaseError(ctx, err)
	}
	var count int
	err := tx.QueryRow(ctx, "SELECT count(*) FROM community_reaction_requests WHERE principal_id=$1 AND created_at>=clock_timestamp()-interval '1 minute'", authn.Actor(ctx)).Scan(&count)
	if err != nil {
		return databaseError(ctx, err)
	}
	if count >= 60 {
		return status.Error(codes.ResourceExhausted, "Не более 60 новых операций реакций в минуту")
	}
	return nil
}

func applyCommunityReaction(ctx context.Context, tx pgx.Tx, req *pb.SetCommunityReactionRequest, current *pb.CommunityReactionState) error {
	if current.Revision != req.ExpectedRevision {
		return status.Error(codes.Aborted, "Реакции изменились; обновите состояние")
	}
	selected := slices.IndexFunc(current.Reactions, func(r *pb.CommunityReaction) bool { return r.Emoji == req.Emoji })
	present := selected >= 0 && current.Reactions[selected].Selected
	if present == req.Active {
		return nil
	}
	if req.Active && selected < 0 && len(current.Reactions) >= 32 {
		return status.Error(codes.ResourceExhausted, "Не более 32 разных реакций")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO community_reaction_targets(channel_id,kind,target_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, req.ChannelId, int32(req.Content.Kind), req.Content.Id); err != nil {
		return databaseError(ctx, err)
	}
	query := `DELETE FROM community_reactions WHERE channel_id=$1 AND kind=$2 AND target_id=$3 AND emoji=$4 AND principal_id=$5`
	if req.Active {
		query = `INSERT INTO community_reactions(channel_id,kind,target_id,emoji,principal_id) VALUES($1,$2,$3,$4,$5)`
	}
	if _, err := tx.Exec(ctx, query, req.ChannelId, int32(req.Content.Kind), req.Content.Id, req.Emoji, authn.Actor(ctx)); err != nil {
		return databaseError(ctx, err)
	}
	_, err := tx.Exec(ctx, "UPDATE community_reaction_targets SET revision=revision+1 WHERE channel_id=$1 AND kind=$2 AND target_id=$3", req.ChannelId, int32(req.Content.Kind), req.Content.Id)
	return databaseErrorOrNil(ctx, err)
}

func commitReaction(ctx context.Context, tx pgx.Tx, req *pb.SetCommunityReactionRequest, state *pb.CommunityReactionState, hash []byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO community_reaction_requests(principal_id,channel_id,idempotency_key,request_hash,applied_revision) VALUES($1,$2,$3,$4,$5)`, authn.Actor(ctx), req.ChannelId, req.IdempotencyKey, hash, state.Revision)
	if err != nil {
		return databaseError(ctx, err)
	}
	return databaseErrorOrNil(ctx, tx.Commit(ctx))
}

func (s *Store) SetCommunityReaction(ctx context.Context, req *pb.SetCommunityReactionRequest) (*pb.SetCommunityReactionResponse, error) {
	if err := validateReactionChange(req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, c, err := s.reactionTransaction(ctx, req.ChannelId, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	closed, err := reactionTarget(ctx, tx, c, req.Content)
	if err != nil {
		return nil, err
	}
	if c.Archived || closed {
		return nil, status.Error(codes.FailedPrecondition, "Тема закрыта или раздел архивирован")
	}
	allowed, err := reactionWriteAllowed(ctx, tx, c)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, forbidden()
	}
	encoded, _ := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	hash := sha256.Sum256(encoded)
	applied, replayed, err := reactionReplay(ctx, tx, req, hash[:])
	if err != nil {
		return nil, err
	}
	current, err := reactionState(ctx, tx, c.Id, req.Content, true)
	if err != nil {
		return nil, err
	}
	if replayed {
		return &pb.SetCommunityReactionResponse{State: current, IdempotencyKey: req.IdempotencyKey, AppliedRevision: applied}, nil
	}
	if err = reactionBudget(ctx, tx); err != nil {
		return nil, err
	}
	if err = applyCommunityReaction(ctx, tx, req, current); err != nil {
		return nil, err
	}
	current, err = reactionState(ctx, tx, c.Id, req.Content, true)
	if err != nil {
		return nil, err
	}
	if err = commitReaction(ctx, tx, req, current, hash[:]); err != nil {
		return nil, err
	}
	return &pb.SetCommunityReactionResponse{State: current, IdempotencyKey: req.IdempotencyKey, AppliedRevision: current.Revision}, nil
}
