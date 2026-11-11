// Package builtin связывает внутренний профиль P2 с существующими сервисами.
package builtin

import (
	"context"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type Installation struct{ ID, PluginID, Version string }
type Section struct {
	InstanceID   string
	ViewID       string
	Installation Installation
	Channel      *pb.Channel
}

type Backend interface {
	BuiltinSections(context.Context) ([]Section, error)
	BuiltinSection(context.Context, string) (Section, error)
	BuiltinReleaseSection(context.Context, string) (Section, error)
	List(context.Context, *pb.ListContentRequest) (*pb.ListContentResponse, error)
	ListMlsEnvelopes(context.Context, *pb.ListMlsEnvelopesRequest) (*pb.ListMlsEnvelopesResponse, error)
	ListForumThreads(context.Context, *pb.ListForumThreadsRequest) (*pb.ListForumThreadsResponse, error)
	ListFeedPosts(context.Context, *pb.ListFeedPostsRequest) (*pb.ListFeedPostsResponse, error)
}
type Rooms interface {
	GetVoiceRoom(context.Context, *pb.GetVoiceRoomRequest) (*pb.GetVoiceRoomResponse, error)
}
type Result struct {
	Section Section
	Content proto.Message
}
type reader func(context.Context, *pb.Channel) (proto.Message, error)

// Registry разрешает экземпляры для чтения и команд рабочего RPC.
type Registry struct {
	backend Backend
	readers map[string]reader
}

func New(backend Backend, rooms Rooms) *Registry {
	return &Registry{backend: backend, readers: map[string]reader{
		"space.chat": func(ctx context.Context, channel *pb.Channel) (proto.Message, error) {
			if len(channel.MlsGroupId) != 0 {
				return backend.ListMlsEnvelopes(ctx, &pb.ListMlsEnvelopesRequest{ChannelId: channel.Id})
			}
			return backend.List(ctx, &pb.ListContentRequest{ChannelId: channel.Id})
		},
		"space.discussions": func(ctx context.Context, channel *pb.Channel) (proto.Message, error) {
			return backend.ListForumThreads(ctx, &pb.ListForumThreadsRequest{ChannelId: channel.Id, Limit: 20})
		},
		"space.feed": func(ctx context.Context, channel *pb.Channel) (proto.Message, error) {
			return backend.ListFeedPosts(ctx, &pb.ListFeedPostsRequest{ChannelId: channel.Id, Limit: 20})
		},
		"space.rooms": func(ctx context.Context, channel *pb.Channel) (proto.Message, error) {
			if rooms == nil {
				return nil, status.Error(codes.Unavailable, "Служба комнат недоступна")
			}
			return rooms.GetVoiceRoom(ctx, &pb.GetVoiceRoomRequest{ChannelId: channel.Id})
		},
	}}
}

func (r *Registry) List(ctx context.Context) ([]Section, error) {
	return r.backend.BuiltinSections(ctx)
}

func (r *Registry) Open(ctx context.Context, instance string) (*Result, error) {
	section, err := r.backend.BuiltinSection(ctx, instance)
	if err != nil {
		return nil, err
	}
	read, ok := r.readers[section.Installation.PluginID]
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "Встроенный адаптер не поддерживается")
	}
	content, err := read(ctx, section.Channel)
	if err != nil {
		return nil, err
	}
	return &Result{Section: section, Content: content}, nil
}
