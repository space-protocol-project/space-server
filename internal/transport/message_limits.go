package transport

import (
	"context"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// MaxRPCRequestBytes ограничивает выделение памяти при декодировании внутренним мостом.
const MaxRPCRequestBytes = 1024 * 1024

func requestMessageLimit(method string) int {
	if method == rpc.RemoteBackupServicePutBackupProcedure { return 6*1024*1024 }
	if method == rpc.DirectoryPairingServiceWriteDirectoryFrameProcedure {
		return 96 * 1024
	}
	if method == rpc.FeedServiceCreateFeedPostProcedure || method == rpc.FeedServiceCommentFeedPostProcedure || method == rpc.FeedServiceUpdateFeedAccessProcedure || method == rpc.ForumServiceCreateForumThreadProcedure || method == rpc.ForumServiceReplyForumThreadProcedure {
		return 64 * 1024
	}
	if method == rpc.MlsServicePublishMlsProcedure {
		return MaxRPCRequestBytes
	}
	if method == rpc.MlsServiceRegisterMlsDeviceProcedure {
		return 96 * 1024
	}
	if method == rpc.AssetServiceUploadChunkProcedure {
		return 384 * 1024
	}
	return 16 * 1024
}

func checkMessageSize(method string, request any) error {
	message, ok := request.(proto.Message)
	if !ok {
		return status.Error(codes.Internal, "Некорректный тип RPC-запроса")
	}
	if proto.Size(message) > requestMessageLimit(method) {
		return status.Error(codes.ResourceExhausted, "Превышен размер RPC-запроса")
	}
	return nil
}

// MessageSizeLimit сохраняет предел 16 КиБ для всех unary RPC, кроме UploadChunk.
func MessageSizeLimit(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := checkMessageSize(info.FullMethod, request); err != nil {
		return nil, err
	}
	return handler(ctx, request)
}

type limitedServerStream struct {
	grpc.ServerStream
	method string
}

func (s limitedServerStream) RecvMsg(message any) error {
	if err := s.ServerStream.RecvMsg(message); err != nil {
		return err
	}
	return checkMessageSize(s.method, message)
}

// StreamMessageSizeLimit ограничивает запросы к потоковым RPC теми же правилами.
func StreamMessageSizeLimit(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return handler(server, limitedServerStream{stream, info.FullMethod})
}
