package chat

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mutationAcknowledgement interface {
	GetContent() *pb.Content
	GetIdempotencyKey() string
	GetAppliedRevision() int64
}

func mutationResponse(ack mutationAcknowledgement, err error) (*pb.ApplyContentMutationResponse, error) {
	if err != nil {
		return nil, err
	}
	return &pb.ApplyContentMutationResponse{Content: ack.GetContent(),
		IdempotencyKey: ack.GetIdempotencyKey(), AppliedRevision: ack.GetAppliedRevision()}, nil
}

func (s *Service) ApplyContentMutation(ctx context.Context, req *pb.ApplyContentMutationRequest) (*pb.ApplyContentMutationResponse, error) {
	missing := status.Error(codes.InvalidArgument, "Нужен ключ изменения")
	switch action := req.Action.(type) {
	case *pb.ApplyContentMutationRequest_Update:
		if action.Update.GetIdempotencyKey() == "" {
			return nil, missing
		}
		return mutationResponse(s.UpdateContent(ctx, action.Update))
	case *pb.ApplyContentMutationRequest_Delete:
		if action.Delete.GetIdempotencyKey() == "" {
			return nil, missing
		}
		return mutationResponse(s.DeleteContent(ctx, action.Delete))
	case *pb.ApplyContentMutationRequest_Reaction:
		if action.Reaction.GetIdempotencyKey() == "" {
			return nil, missing
		}
		return mutationResponse(s.SetReaction(ctx, action.Reaction))
	default:
		return nil, status.Error(codes.InvalidArgument, "Нужно действие изменения")
	}
}
