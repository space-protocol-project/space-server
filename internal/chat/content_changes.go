package chat

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) UpdateContent(ctx context.Context, req *pb.UpdateContentRequest) (*pb.UpdateContentResponse, error) {
	if req.Id == "" || req.ExpectedRevision < 1 || len(req.Text) > 4096 {
		return nil, status.Error(codes.InvalidArgument, "Нужны сообщение, его версия и текст до 4096 байт")
	}
	store, ok := s.store.(interface {
		Update(context.Context, *pb.UpdateContentRequest) (*pb.UpdateContentResponse, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "Изменения требуют авторизованного постоянного хранилища")
	}
	return store.Update(ctx, req)
}

func (s *Service) DeleteContent(ctx context.Context, req *pb.DeleteContentRequest) (*pb.DeleteContentResponse, error) {
	if req.Id == "" || req.ExpectedRevision < 1 {
		return nil, status.Error(codes.InvalidArgument, "Нужны сообщение и его версия")
	}
	store, ok := s.store.(interface {
		Delete(context.Context, *pb.DeleteContentRequest) (*pb.DeleteContentResponse, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "Изменения требуют авторизованного постоянного хранилища")
	}
	return store.Delete(ctx, req)
}
