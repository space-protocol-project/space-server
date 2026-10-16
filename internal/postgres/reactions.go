package postgres

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/reactions"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"slices"
)

func (s *Store) SetReaction(ctx context.Context, req *pb.SetReactionRequest) (*pb.SetReactionResponse, error) {
	if !reactions.Valid(req.Emoji) || req.ExpectedRevision < 1 {
		return nil, status.Error(codes.InvalidArgument, "Недопустимая реакция")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err = s.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	if err = requireOpenChannelAccess(ctx, tx, req.ChannelId, "write"); err != nil {
		return nil, err
	}
	content, err := loadContent(ctx, tx, req.ChannelId, req.Id)
	if err != nil {
		return nil, err
	}
	if content.Deleted {
		return nil, status.Error(codes.FailedPrecondition, "Сообщение удалено")
	}
	actor := authn.Actor(ctx)
	selected := findReaction(content, req.Emoji)
	present := selected != nil && slices.Contains(selected.PrincipalIds, actor)
	// Повтор не создаёт событие; устаревшая операция не отменяет новое решение.
	if present == req.Active {
		return &pb.SetReactionResponse{Content: content}, nil
	}
	if content.Revision != req.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "Реакции изменились. Повторите действие")
	}
	if err = changeReaction(content, selected, req, actor); err != nil {
		return nil, err
	}
	content.Revision++
	if err = saveContentChange(ctx, tx, content); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.SetReactionResponse{Content: content}, nil
}

func findReaction(content *pb.Content, emoji string) *pb.Reaction {
	for _, reaction := range content.Reactions {
		if reaction.Emoji == emoji {
			return reaction
		}
	}
	return nil
}

func changeReaction(content *pb.Content, selected *pb.Reaction, req *pb.SetReactionRequest, actor string) error {
	if !req.Active {
		selected.PrincipalIds = slices.DeleteFunc(selected.PrincipalIds, func(id string) bool { return id == actor })
		content.Reactions = slices.DeleteFunc(content.Reactions, func(r *pb.Reaction) bool { return len(r.PrincipalIds) == 0 })
		return nil
	}
	if selected == nil {
		if len(content.Reactions) >= 32 {
			return status.Error(codes.ResourceExhausted, "Не более 32 разных реакций")
		}
		selected = &pb.Reaction{Emoji: req.Emoji}
		content.Reactions = append(content.Reactions, selected)
	}
	if len(selected.PrincipalIds) >= 256 {
		return status.Error(codes.ResourceExhausted, "Лимит участников реакции")
	}
	selected.PrincipalIds = append(selected.PrincipalIds, actor)
	return nil
}
