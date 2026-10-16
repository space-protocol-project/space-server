package chat

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/reactions"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) SetReaction(ctx context.Context, req *pb.SetReactionRequest) (*pb.SetReactionResponse, error) {
	if req.Id == "" || req.ExpectedRevision < 1 || !reactions.Valid(req.Emoji) {
		return nil, status.Error(codes.InvalidArgument, "Нужны сообщение, версия и поддерживаемый эмодзи")
	}
	store, ok := s.store.(interface {
		SetReaction(context.Context, *pb.SetReactionRequest) (*pb.SetReactionResponse, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "Реакции требуют постоянного хранилища")
	}
	return store.SetReaction(ctx, req)
}
