package postgres

import (
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"time"
)

func (s *Store) Subscribe(req *pb.SubscribeRequest, stream pb.SyncService_SubscribeServer) error {
	select {
	case s.streamSlots <- struct{}{}:
		defer func() { <-s.streamSlots }()
	default:
		return status.Error(codes.ResourceExhausted, "Лимит: 64 активные подписки")
	}
	ctx := stream.Context()
	token := authn.Token(ctx)
	if token == "" {
		return status.Error(codes.Unauthenticated, "Нужна проверенная сессия")
	}
	cursor := req.After
	batch, err := s.ListEvents(ctx, &pb.ListEventsRequest{ChannelId: req.ChannelId, After: cursor})
	if err != nil {
		return err
	}
	if err := sendFrame(stream, &pb.SubscribeResponse{Cursor: cursor, Heartbeat: true}); err != nil {
		return err
	}
	beat := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := s.Authenticate(ctx, token); err != nil {
			return err
		}
		for _, event := range batch.Events {
			if _, err := s.Authenticate(ctx, token); err != nil {
				return err
			}
			if err := sendFrame(stream, &pb.SubscribeResponse{Event: event, Cursor: event.Cursor}); err != nil {
				return err
			}
			cursor = event.Cursor
			beat = time.Now()
		}
		if time.Since(beat) >= 15*time.Second {
			if err := sendFrame(stream, &pb.SubscribeResponse{Cursor: cursor, Heartbeat: true}); err != nil {
				return err
			}
			beat = time.Now()
		}
		if len(batch.Events) < 100 {
			select {
			case <-ctx.Done():
				return status.FromContextError(ctx.Err()).Err()
			case <-ticker.C:
			}
		}
		batch, err = s.ListEvents(ctx, &pb.ListEventsRequest{ChannelId: req.ChannelId, After: cursor})
		if err != nil {
			return err
		}
	}
}

// В каждый момент одна отправка. Возврат handler закрывает transport context,
// поэтому зависшая Send завершается после deadline/cancel без роста очереди.
func sendFrame(stream pb.SyncService_SubscribeServer, frame *pb.SubscribeResponse) error {
	result := make(chan error, 1)
	go func() { result <- stream.Send(frame) }()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	select {
	case err := <-result:
		return err
	case <-stream.Context().Done():
		return status.FromContextError(stream.Context().Err()).Err()
	case <-timeout.C:
		return status.Error(codes.DeadlineExceeded, "Клиент слишком медленно читает поток")
	}
}
