package chat

import (
	"context"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type channelStore interface {
	ListChannels(context.Context, *pb.ListChannelsRequest) (*pb.ListChannelsResponse, error)
	CreateChannel(context.Context, *pb.CreateChannelRequest) (*pb.CreateChannelResponse, error)
	UpdateChannel(context.Context, *pb.UpdateChannelRequest) (*pb.UpdateChannelResponse, error)
	GetChannelAccess(context.Context, *pb.GetChannelAccessRequest) (*pb.GetChannelAccessResponse, error)
	UpdateChannelAccess(context.Context, *pb.UpdateChannelAccessRequest) (*pb.UpdateChannelAccessResponse, error)
}

func (s *Service) channels() (channelStore, error) {
	store, ok := s.store.(channelStore)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "Каналы и ACL требуют постоянного сервера PostgreSQL")
	}
	return store, nil
}
func (s *Service) ListChannels(ctx context.Context, req *pb.ListChannelsRequest) (*pb.ListChannelsResponse, error) {
	store, err := s.channels()
	if err != nil {
		return nil, err
	}
	return store.ListChannels(ctx, req)
}
func (s *Service) CreateChannel(ctx context.Context, req *pb.CreateChannelRequest) (*pb.CreateChannelResponse, error) {
	store, err := s.channels()
	if err != nil {
		return nil, err
	}
	return store.CreateChannel(ctx, req)
}
func (s *Service) UpdateChannel(ctx context.Context, req *pb.UpdateChannelRequest) (*pb.UpdateChannelResponse, error) {
	store, err := s.channels()
	if err != nil {
		return nil, err
	}
	return store.UpdateChannel(ctx, req)
}
func (s *Service) GetChannelAccess(ctx context.Context, req *pb.GetChannelAccessRequest) (*pb.GetChannelAccessResponse, error) {
	store, err := s.channels()
	if err != nil {
		return nil, err
	}
	return store.GetChannelAccess(ctx, req)
}
func (s *Service) UpdateChannelAccess(ctx context.Context, req *pb.UpdateChannelAccessRequest) (*pb.UpdateChannelAccessResponse, error) {
	store, err := s.channels()
	if err != nil {
		return nil, err
	}
	return store.UpdateChannelAccess(ctx, req)
}

func (s *Service) WatchChannels(req *pb.WatchChannelsRequest, stream pb.ChannelService_WatchChannelsServer) error {
	source, ok := s.store.(interface {
		WatchChannels(*pb.WatchChannelsRequest, pb.ChannelService_WatchChannelsServer) error
	})
	if !ok {
		return status.Error(codes.Unimplemented, "Подписка каналов требует PostgreSQL")
	}
	return source.WatchChannels(req, stream)
}
