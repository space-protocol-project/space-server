package postgres

import (
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"time"
)

// Отправляем только авторизованный снимок. Locks удерживаются до Send,
// поэтому отзыв ACL не может обогнать отправку приватных метаданных.
func (s *Store) catalogFrame(req *pb.WatchChannelsRequest, stream pb.ChannelService_WatchChannelsServer, previous *pb.WatchChannelsResponse, heartbeat bool) (*pb.WatchChannelsResponse, bool, error) {
	ctx := stream.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err = s.lockSession(ctx, tx); err != nil {
		return nil, false, err
	}
	frame := &pb.WatchChannelsResponse{Icon: new(pb.ServerIcon)}
	err = tx.QueryRow(ctx, "SELECT s.title,i.server_id,s.icon_preset,s.icon_media_type,s.icon_data FROM space_settings s CROSS JOIN server_state i WHERE s.singleton=true AND i.singleton=true FOR SHARE OF s").Scan(&frame.SpaceTitle, &frame.ServerId, &frame.Icon.Preset, &frame.Icon.MediaType, &frame.Icon.Data)
	if err != nil {
		return nil, false, databaseError(ctx, err)
	}
	member, err := membership(ctx, tx, authn.Actor(ctx), true)
	if err != nil {
		return nil, false, err
	}
	if member.Blocked {
		return nil, false, forbidden()
	}
	frame.Role = member.Role
	frame.Channels, err = s.channelList(ctx, tx, req.IncludeArchived, false, true)
	if err != nil {
		return nil, false, err
	}
	changed := !proto.Equal(frame, previous)
	if !changed && !heartbeat {
		return previous, false, nil
	}
	outgoing := frame
	if !changed {
		outgoing = &pb.WatchChannelsResponse{Heartbeat: true, ServerId: frame.ServerId}
	}
	if err = sendWithDeadline(ctx, func() error { return stream.Send(outgoing) }); err != nil {
		return nil, false, err
	}
	return frame, true, nil
}

func (s *Store) WatchChannels(req *pb.WatchChannelsRequest, stream pb.ChannelService_WatchChannelsServer) error {
	select {
	case s.streamSlots <- struct{}{}:
		defer func() { <-s.streamSlots }()
	default:
		return status.Error(codes.ResourceExhausted, "Лимит: 64 активные подписки")
	}
	if authn.Token(stream.Context()) == "" {
		return status.Error(codes.Unauthenticated, "Нужна проверенная сессия")
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var previous *pb.WatchChannelsResponse
	beat := time.Time{}
	for {
		snapshot, sent, err := s.catalogFrame(req, stream, previous, time.Since(beat) >= 15*time.Second)
		if err != nil {
			return err
		}
		previous = snapshot
		if sent {
			beat = time.Now()
		}
		select {
		case <-stream.Context().Done():
			return status.FromContextError(stream.Context().Err()).Err()
		case <-ticker.C:
		}
	}
}
