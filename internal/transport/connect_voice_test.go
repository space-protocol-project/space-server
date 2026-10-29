package transport

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type voiceLimitBackend struct {
	pb.UnimplementedVoiceServiceServer
	joined atomic.Int32
}

func (s *voiceLimitBackend) JoinVoiceRoom(ctx context.Context, req *pb.JoinVoiceRoomRequest) (*pb.JoinVoiceRoomResponse, error) {
	s.joined.Add(1)
	return &pb.JoinVoiceRoomResponse{SessionId: authn.Actor(ctx), AnswerSdp: req.OfferSdp}, nil
}

// Проверяет оба кодека, внешний HTTP-предел, Connect и внутренний gRPC interceptor.
func TestConnectVoiceSDPRequestLimit(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backend := &voiceLimitBackend{}
	server := grpc.NewServer(grpc.ChainUnaryInterceptor(authn.Interceptor(connectVerifier{}), MessageSizeLimit))
	pb.RegisterVoiceServiceServer(server, backend)
	go server.Serve(listener)
	defer server.Stop()
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	web := httptest.NewServer(WithConnect(http.NotFoundHandler(), connection, connectVerifier{}))
	defer web.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Реалистичные строки SDP с CRLF; размер превышает прежние 16 КиБ.
	sdp := strings.Repeat("a=rtpmap:111 opus/48000/2\r\n", 2400)
	for _, options := range [][]connect.ClientOption{nil, {connect.WithProtoJSON()}} {
		client := rpc.NewVoiceServiceClient(web.Client(), web.URL, options...)
		request := connect.NewRequest(&pb.JoinVoiceRoomRequest{ChannelId: "room", OfferSdp: sdp})
		request.Header().Set("Authorization", "Bearer valid-token")
		reply, err := client.JoinVoiceRoom(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if reply.Msg.AnswerSdp != sdp || reply.Msg.SessionId != "u_test" {
			t.Fatal("SDP или авторизация изменены мостом")
		}
		request.Header().Set("Authorization", "Bearer wrong")
		if _, err := client.JoinVoiceRoom(ctx, request); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("Обойдена авторизация: %v", err)
		}
		request.Header().Set("Authorization", "Bearer valid-token")
		request.Msg.OfferSdp = strings.Repeat("x", voiceJoinRequestBytes)
		if _, err := client.JoinVoiceRoom(ctx, request); err == nil {
			t.Fatal("Превышение размера принято")
		}
	}
	if backend.joined.Load() != 2 {
		t.Fatal("Недопустимый запрос достиг службы")
	}
}
