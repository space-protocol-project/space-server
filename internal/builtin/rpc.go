package builtin

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

type command struct {
	plugin string
	call   func(context.Context, string, *anypb.Any) (proto.Message, error)
}

// Service передаёт команды тем же экземплярам служб, что обслуживают прежний RPC.
type Service struct {
	pb.UnimplementedBuiltinExtensionServiceServer
	registry *Registry
	commands map[string]command
}

type channelCommand interface {
	proto.Message
	GetChannelId() string
}

func commandChannel(request proto.Message) string {
	if scoped, ok := request.(channelCommand); ok {
		return scoped.GetChannelId()
	}
	if mutation, ok := request.(*pb.ApplyContentMutationRequest); ok {
		switch action := mutation.Action.(type) {
		case *pb.ApplyContentMutationRequest_Update:
			return action.Update.GetChannelId()
		case *pb.ApplyContentMutationRequest_Delete:
			return action.Delete.GetChannelId()
		case *pb.ApplyContentMutationRequest_Reaction:
			return action.Reaction.GetChannelId()
		}
	}
	return ""
}

func bind[I proto.Message, O proto.Message](s *Service, plugin, method string, request I, call func(context.Context, I) (O, error)) {
	key := "type.googleapis.com/" + string(request.ProtoReflect().Descriptor().FullName())
	s.commands[key] = command{plugin, func(ctx context.Context, channel string, payload *anypb.Any) (proto.Message, error) {
		input := request.ProtoReflect().New().Interface().(I)
		if err := payload.UnmarshalTo(input); err != nil {
			return nil, status.Error(codes.InvalidArgument, "Некорректная команда")
		}
		if commandChannel(input) != channel {
			return nil, status.Error(codes.InvalidArgument, "Канал команды не совпадает с экземпляром")
		}
		if err := transport.CheckMessageSize(method, input); err != nil {
			return nil, err
		}
		return call(ctx, input)
	}}
}

func sectionMessage(s Section) *pb.BuiltinSection {
	return &pb.BuiltinSection{InstanceId: s.InstanceID, InstallationId: s.Installation.ID, PluginId: s.Installation.PluginID, Version: s.Installation.Version, ViewId: s.ViewID, Channel: s.Channel}
}

func (s *Service) ListBuiltinSections(ctx context.Context, req *pb.ListBuiltinSectionsRequest) (*pb.ListBuiltinSectionsResponse, error) {
	sections, err := s.registry.List(ctx)
	if err != nil {
		return nil, err
	}
	result := &pb.ListBuiltinSectionsResponse{}
	for _, section := range sections {
		result.Sections = append(result.Sections, sectionMessage(section))
	}
	return result, nil
}

func (s *Service) OpenBuiltinSection(ctx context.Context, req *pb.OpenBuiltinSectionRequest) (*pb.OpenBuiltinSectionResponse, error) {
	result, err := s.registry.Open(ctx, req.GetInstanceId())
	if err != nil {
		return nil, err
	}
	content, err := anypb.New(result.Content)
	if err != nil {
		return nil, err
	}
	return &pb.OpenBuiltinSectionResponse{Section: sectionMessage(result.Section), Content: content}, nil
}

func (s *Service) ExecuteBuiltinCommand(ctx context.Context, req *pb.ExecuteBuiltinCommandRequest) (*pb.ExecuteBuiltinCommandResponse, error) {
	resolve := s.registry.backend.BuiltinSection
	if req.GetCommand().GetTypeUrl() == "type.googleapis.com/space.v1.LeaveVoiceRoomRequest" {
		resolve = s.registry.backend.BuiltinReleaseSection
	}
	section, err := resolve(ctx, req.GetInstanceId())
	if err != nil {
		return nil, err
	}
	command, ok := s.commands[req.GetCommand().GetTypeUrl()]
	if !ok || command.plugin != section.Installation.PluginID {
		return nil, status.Error(codes.InvalidArgument, "Команда не поддерживается экземпляром")
	}
	result, err := command.call(ctx, section.Channel.Id, req.Command)
	if err != nil {
		return nil, err
	}
	payload, err := anypb.New(result)
	if err != nil {
		return nil, err
	}
	return &pb.ExecuteBuiltinCommandResponse{Result: payload}, nil
}
