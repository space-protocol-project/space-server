package transport

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

func TestUnaryMessageLimit(t *testing.T) {
	cases := []struct {
		name, method string
		message      proto.Message
		want         codes.Code
	}{
		{"Голосовой SDP", rpc.VoiceServiceJoinVoiceRoomProcedure, &pb.JoinVoiceRoomRequest{OfferSdp: strings.Repeat("x", 64*1024)}, codes.OK},
		{"Слишком большой голосовой запрос", rpc.VoiceServiceJoinVoiceRoomProcedure, &pb.JoinVoiceRoomRequest{OfferSdp: strings.Repeat("x", 96*1024)}, codes.ResourceExhausted},
		{"Предел остальных голосовых запросов", rpc.VoiceServiceUpdateVoiceRoomProcedure, &pb.UpdateVoiceRoomRequest{ChannelId: strings.Repeat("x", 17*1024)}, codes.ResourceExhausted},
		{"MLS Commit", rpc.MlsServicePublishMlsProcedure, &pb.PublishMlsRequest{Ciphertext: make([]byte, 256*1024)}, codes.OK},
		{"Слишком большой MLS", rpc.MlsServicePublishMlsProcedure, &pb.PublishMlsRequest{Ciphertext: make([]byte, MaxRPCRequestBytes)}, codes.ResourceExhausted},
		{"MLS KeyPackage", rpc.MlsServiceRegisterMlsDeviceProcedure, &pb.RegisterMlsDeviceRequest{KeyPackage: make([]byte, 64*1024)}, codes.OK},
		{"Обычное сообщение", rpc.ContentServiceCreateContentProcedure, &pb.CreateContentRequest{Text: "text"}, codes.OK},
		{"Большая часть файла", rpc.AssetServiceUploadChunkProcedure, &pb.UploadChunkRequest{Data: make([]byte, 256*1024)}, codes.OK},
		{"Слишком большая часть", rpc.AssetServiceUploadChunkProcedure, &pb.UploadChunkRequest{Data: make([]byte, MaxRPCRequestBytes)}, codes.ResourceExhausted},
		{"Предел остальных запросов", rpc.ContentServiceCreateContentProcedure, &pb.CreateContentRequest{Text: strings.Repeat("x", 17*1024)}, codes.ResourceExhausted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called := false
			_, err := MessageSizeLimit(context.Background(), c.message, &grpc.UnaryServerInfo{FullMethod: c.method}, func(context.Context, any) (any, error) { called = true; return nil, nil })
			if status.Code(err) != c.want {
				t.Fatal(err)
			}
			if called != (c.want == codes.OK) {
				t.Fatal("Нарушен порядок проверки размера")
			}
		})
	}
}

type incomingTestStream struct {
	grpc.ServerStream
	payload *pb.SubscribeRequest
}

func (s incomingTestStream) RecvMsg(value any) error {
	proto.Merge(value.(proto.Message), s.payload)
	return nil
}
func TestStreamMessageLimit(t *testing.T) {
	stream := incomingTestStream{payload: &pb.SubscribeRequest{After: strings.Repeat("x", 17*1024)}}
	wrapped := limitedServerStream{ServerStream: stream, method: rpc.SyncServiceSubscribeProcedure}
	if err := wrapped.RecvMsg(new(pb.SubscribeRequest)); status.Code(err) != codes.ResourceExhausted {
		t.Fatal(err)
	}
}
