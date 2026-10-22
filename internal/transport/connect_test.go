package transport

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type connectVerifier struct{}

func (connectVerifier) Authenticate(_ context.Context, token string) (string, error) {
	if token != "valid-token" {
		return "", status.Error(codes.Unauthenticated, "Недействительный token")
	}
	return "u_test", nil
}

func TestConnectAuthorizesBeforeDecodingAllProtectedServices(t *testing.T) {
	handler := WithConnect(http.NotFoundHandler(), nil, connectVerifier{})
	for _, path := range []string{
		rpc.ChannelServiceListChannelsProcedure,
		rpc.ChannelServiceWatchChannelsProcedure,
		rpc.ContentServiceCreateContentProcedure,
		rpc.ForumServiceCreateForumThreadProcedure,
		rpc.ForumServiceReplyForumThreadProcedure,
		rpc.ForumServiceCloseForumThreadProcedure,
		rpc.ForumServiceListForumThreadsProcedure,
		rpc.ForumServiceGetForumThreadProcedure,
		rpc.ContentServiceSearchContentProcedure,
		rpc.ContentServiceGetHistoryPageProcedure,
		rpc.ContentServiceSetReactionProcedure,
		rpc.ContentServiceApplyContentMutationProcedure,
		rpc.ContentServiceUpdateContentProcedure,
		rpc.ContentServiceDeleteContentProcedure,
		rpc.AuthServiceListDevicesProcedure,
		rpc.SyncServiceSubscribeProcedure,
		rpc.AdminServiceUpdateSettingsProcedure,
		rpc.MembershipServiceGetMembershipProcedure,
		rpc.MembershipServiceUpdateProfileProcedure,
		rpc.AssetServiceBeginUploadProcedure,
		rpc.AssetServiceUploadChunkProcedure,
		rpc.AssetServiceCompleteUploadProcedure,
		rpc.AssetServiceCancelUploadProcedure,
		rpc.AssetServiceDownloadChunkProcedure,
		rpc.AssetServiceListMyAssetsProcedure,
		rpc.AssetServiceListChannelAssetsProcedure,
		rpc.MlsServiceRegisterMlsDeviceProcedure,
		rpc.MlsServiceListMlsDevicesProcedure,
		rpc.MlsServiceGetMlsChannelProcedure,
		rpc.MlsServicePublishMlsProcedure,
		rpc.MlsServiceListMlsEnvelopesProcedure,
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("не JSON"))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Connect-Protocol-Version", "1")
			request.Header.Set("Authorization", "Bearer wrong")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "unauthenticated") {
				t.Fatalf("Авторизация выполняется после декодирования: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestConnectProcedureRegistryCoversServices(t *testing.T) {
	expected := 0
	files := []protoreflect.FileDescriptor{pb.File_space_v1_space_proto, pb.File_space_v1_voice_proto, pb.File_space_v1_forum_proto}
	for _, file := range files {
		services := file.Services()
		for i := 0; i < services.Len(); i++ {
			service := services.Get(i)
			for j := 0; j < service.Methods().Len(); j++ {
				path := "/" + string(service.FullName()) + "/" + string(service.Methods().Get(j).Name())
				if !isConnectProcedure(path) {
					t.Errorf("Не зарегистрирована процедура %s", path)
				}
				expected++
			}
		}
	}
	if len(connectProcedures) != expected {
		t.Fatalf("Неполный набор Connect RPC: %d", len(connectProcedures))
	}
	if isConnectProcedure("/space.v1.AdminService/Unknown") {
		t.Fatal("Неизвестная процедура разрешена")
	}
}

type connectBackend struct {
	pb.UnimplementedChannelServiceServer
	pb.UnimplementedSyncServiceServer
	cancelled chan struct{}
}

func (s *connectBackend) GetManifest(ctx context.Context, _ *pb.GetManifestRequest) (*pb.GetManifestResponse, error) {
	return &pb.GetManifestResponse{ServerId: authn.Actor(ctx)}, nil
}
func (s *connectBackend) DeleteChannel(ctx context.Context, req *pb.DeleteChannelRequest) (*pb.DeleteChannelResponse, error) {
	if authn.Actor(ctx) != "u_test" || req.ChannelId != "team" || req.ExpectedRevision != 4 {
		return nil, status.Error(codes.InvalidArgument, "Вызов изменён")
	}
	return &pb.DeleteChannelResponse{}, nil
}
func (s *connectBackend) Subscribe(_ *pb.SubscribeRequest, stream pb.SyncService_SubscribeServer) error {
	if err := stream.Send(&pb.SubscribeResponse{Heartbeat: true}); err != nil {
		return err
	}
	<-stream.Context().Done()
	close(s.cancelled)
	return status.FromContextError(stream.Context().Err()).Err()
}
func TestConnectKeepsInternalAuthAndCancelsStream(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backend := &connectBackend{cancelled: make(chan struct{})}
	server := grpc.NewServer(grpc.UnaryInterceptor(authn.Interceptor(connectVerifier{})), grpc.StreamInterceptor(authn.StreamInterceptor(connectVerifier{})))
	pb.RegisterChannelServiceServer(server, backend)
	pb.RegisterSyncServiceServer(server, backend)
	go server.Serve(listener)
	defer server.Stop()
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	httpServer := httptest.NewServer(WithConnect(http.NotFoundHandler(), connection, connectVerifier{}))
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, options := range [][]connect.ClientOption{nil, {connect.WithProtoJSON()}} {
		client := rpc.NewChannelServiceClient(httpServer.Client(), httpServer.URL, options...)
		req := connect.NewRequest(&pb.GetManifestRequest{})
		req.Header().Set("Authorization", "Bearer valid-token")
		reply, err := client.GetManifest(ctx, req)
		if err != nil || reply.Msg.ServerId != "u_test" {
			t.Fatalf("Manifest: %v %v", reply, err)
		}

		deleteReq := connect.NewRequest(&pb.DeleteChannelRequest{ChannelId: "team", ExpectedRevision: 4})
		deleteReq.Header().Set("Authorization", "Bearer valid-token")
		if _, err := client.DeleteChannel(ctx, deleteReq); err != nil {
			t.Fatal(err)
		}
		deleteReq.Header().Set("Authorization", "Bearer wrong")
		if _, err := client.DeleteChannel(ctx, deleteReq); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("Удаление без авторизации: %v", err)
		}
		req.Header().Set("Authorization", "Bearer wrong")
		if _, err := client.GetManifest(ctx, req); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("Auth обойдена: %v", err)
		}
	}
	sync := rpc.NewSyncServiceClient(httpServer.Client(), httpServer.URL)
	denied, err := sync.Subscribe(ctx, connect.NewRequest(&pb.SubscribeRequest{ChannelId: "general"}))
	if err == nil {
		denied.Receive()
		err = denied.Err()
		denied.Close()
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("Поток без token: %v", err)
	}
	req := connect.NewRequest(&pb.SubscribeRequest{ChannelId: "general"})
	req.Header().Set("Authorization", "Bearer valid-token")
	stream, err := sync.Subscribe(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || !stream.Msg().Heartbeat {
		t.Fatal("Нет heartbeat")
	}
	if err := stream.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-backend.cancelled:
	case <-ctx.Done():
		t.Fatal("Отмена не достигла gRPC")
	}
}
