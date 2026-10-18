package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type connectVoice struct {
	rpc.UnimplementedVoiceServiceHandler
	client pb.VoiceServiceClient
}

func (s connectVoice) GetVoiceConfiguration(ctx context.Context, req *connect.Request[pb.GetVoiceConfigurationRequest]) (*connect.Response[pb.GetVoiceConfigurationResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetVoiceConfiguration)
}

func (s connectVoice) JoinVoiceRoom(ctx context.Context, req *connect.Request[pb.JoinVoiceRoomRequest]) (*connect.Response[pb.JoinVoiceRoomResponse], error) {
	return bridgeUnary(ctx, req, s.client.JoinVoiceRoom)
}
func (s connectVoice) UpdateVoiceRoom(ctx context.Context, req *connect.Request[pb.UpdateVoiceRoomRequest]) (*connect.Response[pb.UpdateVoiceRoomResponse], error) {
	return bridgeUnary(ctx, req, s.client.UpdateVoiceRoom)
}
func (s connectVoice) LeaveVoiceRoom(ctx context.Context, req *connect.Request[pb.LeaveVoiceRoomRequest]) (*connect.Response[pb.LeaveVoiceRoomResponse], error) {
	return bridgeUnary(ctx, req, s.client.LeaveVoiceRoom)
}
func (s connectVoice) GetVoiceRoom(ctx context.Context, req *connect.Request[pb.GetVoiceRoomRequest]) (*connect.Response[pb.GetVoiceRoomResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetVoiceRoom)
}
