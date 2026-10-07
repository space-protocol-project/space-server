package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"

	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Service хранит данные только до завершения процесса.
type Service struct {
	pb.UnimplementedChannelServiceServer
	pb.UnimplementedContentServiceServer
	mu       sync.Mutex
	serverID string
	messages []*pb.Content
	requests map[string]*pb.Content
}

func New(serverID string) *Service {
	return &Service{serverID: serverID, requests: make(map[string]*pb.Content)}
}

func (s *Service) GetManifest(context.Context, *pb.GetManifestRequest) (*pb.GetManifestResponse, error) {
	return &pb.GetManifestResponse{ProtocolVersion: "0.1-experimental", ServerId: s.serverID,
		Channels: []*pb.Channel{{Id: "general", Title: "Общий чат", Views: []*pb.View{{Id: "chat", Type: "chat"}}}}}, nil
}

func validateChannel(id string) error {
	if id != "general" {
		return status.Error(codes.NotFound, "Канал не найден")
	}
	return nil
}

func clone(c *pb.Content) *pb.Content { return proto.Clone(c).(*pb.Content) }

func (s *Service) CreateContent(ctx context.Context, req *pb.CreateContentRequest) (*pb.CreateContentResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if err := validateChannel(req.ChannelId); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Text) == "" || len(req.Text) > 4096 || len(req.IdempotencyKey) == 0 || len(req.IdempotencyKey) > 128 {
		return nil, status.Error(codes.InvalidArgument, "Нужны текст до 4096 байт и ключ идемпотентности до 128 байт")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.requests[req.IdempotencyKey]; ok {
		if previous.Text != req.Text {
			return nil, status.Error(codes.AlreadyExists, "Ключ уже использован с другим текстом")
		}
		return &pb.CreateContentResponse{Content: clone(previous)}, nil
	}
	if len(s.messages) >= 1000 {
		return nil, status.Error(codes.ResourceExhausted, "Лимит прототипа: 1000 сообщений")
	}
	message := &pb.Content{Id: fmt.Sprintf("message-%d", len(s.messages)+1), ChannelId: req.ChannelId, Text: req.Text}
	s.messages = append(s.messages, message)
	s.requests[req.IdempotencyKey] = message
	return &pb.CreateContentResponse{Content: clone(message)}, nil
}

func (s *Service) ListContent(ctx context.Context, req *pb.ListContentRequest) (*pb.ListContentResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if err := validateChannel(req.ChannelId); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	start := 0
	if req.After != "" {
		found := false
		for i, message := range s.messages {
			if message.Id == req.After {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, status.Error(codes.InvalidArgument, "Неизвестный курсор")
		}
	}
	result := &pb.ListContentResponse{NextCursor: req.After}
	end := min(start+100, len(s.messages))
	for _, message := range s.messages[start:end] {
		result.Contents = append(result.Contents, clone(message))
		result.NextCursor = message.Id
	}
	return result, nil
}
