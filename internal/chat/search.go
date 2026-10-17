package chat

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"unicode/utf8"
)

func (s *Service) SearchContent(ctx context.Context, req *pb.SearchContentRequest) (*pb.SearchContentResponse, error) {
	query := strings.TrimSpace(req.Query)
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) < 2 || utf8.RuneCountInString(query) > 128 || len(query) > 512 || req.BeforeSequence < 0 {
		return nil, status.Error(codes.InvalidArgument, "Запрос должен содержать от 2 до 128 символов")
	}
	source, ok := s.store.(interface {
		Search(context.Context, *pb.SearchContentRequest) (*pb.SearchContentResponse, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "Поиск требует постоянного хранилища")
	}
	return source.Search(ctx, req)
}
