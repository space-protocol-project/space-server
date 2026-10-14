package postgres

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
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
	if err := s.sendAuthorizedFrame(req.ChannelId, stream, &pb.SubscribeResponse{Cursor: cursor, Heartbeat: true}); err != nil {
		return err
	}
	beat := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := s.Authenticate(ctx, token); err != nil {
			return err
		}
		cursor, beat, err = s.sendEventBatch(req.ChannelId, stream, batch.Events, cursor, beat)
		if err != nil {
			return err
		}
		beat, err = s.waitForNextEventBatch(ctx, req.ChannelId, stream, cursor, beat, len(batch.Events), ticker)
		if err != nil {
			return err
		}
		batch, err = s.ListEvents(ctx, &pb.ListEventsRequest{ChannelId: req.ChannelId, After: cursor})
		if err != nil {
			return err
		}
	}
}

// Отзыв прав ждёт текущую ограниченную по времени отправку. Следующая отправка
// проверяет новое состояние, включая события из уже прочитанной пачки.
func (s *Store) sendAuthorizedFrame(channel string, stream pb.SyncService_SubscribeServer, frame *pb.SubscribeResponse) error {
	ctx := stream.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err = s.lockSession(ctx, tx); err != nil {
		return err
	}
	if _, err = channelAccess(ctx, tx, channel, "read", true); err != nil {
		return err
	}
	return sendFrame(stream, frame)
}

// В каждый момент одна отправка. Возврат handler закрывает transport context,
// поэтому зависшая Send завершается после deadline/cancel без роста очереди.
func sendFrame(stream pb.SyncService_SubscribeServer, frame *pb.SubscribeResponse) error {
	return sendWithDeadline(stream.Context(), func() error { return stream.Send(frame) })
}

func sendWithDeadline(ctx context.Context, send func() error) error {
	result := make(chan error, 1)
	go func() { result <- send() }()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	case <-timeout.C:
		return status.Error(codes.DeadlineExceeded, "Клиент слишком медленно читает поток")
	}
}

func (s *Store) sendEventBatch(channel string, stream pb.SyncService_SubscribeServer, events []*pb.Event, cursor string, beat time.Time) (string, time.Time, error) {
	for _, event := range events {
		if err := s.sendAuthorizedFrame(channel, stream, &pb.SubscribeResponse{Event: event, Cursor: event.Cursor}); err != nil {
			return cursor, beat, err
		}
		cursor = event.Cursor
		beat = time.Now()
	}
	return cursor, beat, nil
}

func (s *Store) waitForNextEventBatch(ctx context.Context, channel string, stream pb.SyncService_SubscribeServer, cursor string, beat time.Time, batchSize int, ticker *time.Ticker) (time.Time, error) {
	if time.Since(beat) >= 15*time.Second {
		if err := s.sendAuthorizedFrame(channel, stream, &pb.SubscribeResponse{Cursor: cursor, Heartbeat: true}); err != nil {
			return beat, err
		}
		beat = time.Now()
	}
	if batchSize < 100 {
		select {
		case <-ctx.Done():
			return beat, status.FromContextError(ctx.Err()).Err()
		case <-ticker.C:
		}
	}
	return beat, nil
}
