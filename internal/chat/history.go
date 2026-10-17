package chat

import (
	"context"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) GetHistoryPage(ctx context.Context, req *pb.GetHistoryPageRequest) (*pb.GetHistoryPageResponse, error) {
	if req.BeforeSequence < 0 {
		return nil, status.Error(codes.InvalidArgument, "Некорректная граница истории")
	}
	source, ok := s.store.(interface {
		HistoryPage(context.Context, *pb.GetHistoryPageRequest) (*pb.GetHistoryPageResponse, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "Постраничная история требует постоянного хранилища")
	}
	return source.HistoryPage(ctx, req)
}
