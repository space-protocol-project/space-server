package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func reactionState(ctx context.Context, tx pgx.Tx, channel string, ref *pb.CommunityContentReference, allowed bool) (*pb.CommunityReactionState, error) {
	out := &pb.CommunityReactionState{Content: ref, CanReact: allowed}
	err := tx.QueryRow(ctx, "SELECT revision FROM community_reaction_targets WHERE channel_id=$1 AND kind=$2 AND target_id=$3", channel, int32(ref.Kind), ref.Id).Scan(&out.Revision)
	if err != nil && err != pgx.ErrNoRows {
		return nil, databaseError(ctx, err)
	}
	rows, err := tx.Query(ctx, `SELECT emoji,count(*),bool_or(principal_id=$4) FROM community_reactions
 WHERE channel_id=$1 AND kind=$2 AND target_id=$3 GROUP BY emoji ORDER BY emoji`, channel, int32(ref.Kind), ref.Id, authn.Actor(ctx))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		item := new(pb.CommunityReaction)
		if err = rows.Scan(&item.Emoji, &item.Count, &item.Selected); err != nil {
			return nil, databaseError(ctx, err)
		}
		out.Reactions = append(out.Reactions, item)
	}
	return out, databaseErrorOrNil(ctx, rows.Err())
}

func validateCommunityReferences(refs []*pb.CommunityContentReference) error {
	if len(refs) == 0 || len(refs) > 50 {
		return status.Error(codes.InvalidArgument, "Нужно от 1 до 50 объектов")
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if !validCommunityReference(ref) {
			return status.Error(codes.InvalidArgument, "Некорректный объект")
		}
		key := ref.Kind.String() + ":" + ref.Id
		if seen[key] {
			return status.Error(codes.InvalidArgument, "Повтор объекта")
		}
		seen[key] = true
	}
	return nil
}

func (s *Store) ListCommunityReactions(ctx context.Context, req *pb.ListCommunityReactionsRequest) (*pb.ListCommunityReactionsResponse, error) {
	if err := validateCommunityReferences(req.Contents); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, c, err := s.communityContentTransaction(ctx, req.ChannelId, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	allowed, err := reactionWriteAllowed(ctx, tx, c)
	if err != nil {
		return nil, err
	}
	out := new(pb.ListCommunityReactionsResponse)
	for _, ref := range req.Contents {
		closed, err := communityContentTarget(ctx, tx, c, ref)
		if status.Code(err) == codes.NotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		item, err := reactionState(ctx, tx, c.Id, ref, allowed && !closed)
		if err != nil {
			return nil, err
		}
		out.States = append(out.States, item)
	}
	return out, nil
}
