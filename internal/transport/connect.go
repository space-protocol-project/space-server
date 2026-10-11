package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"io"
	"net/http"
	"time"
)

// Прототип передаёт вызовы штатному gRPC-серверу с его проверками grants/ACL.
func prototypeContext(ctx context.Context, header http.Header) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.MD{"authorization": header.Values("Authorization")})
}
func prototypeError(err error) error {
	if err == nil {
		return nil
	}
	return connect.NewError(connect.Code(status.Code(err)), status.Convert(err).Err())
}

type connectChannels struct {
	rpc.UnimplementedChannelServiceHandler
	client pb.ChannelServiceClient
}

func (s connectChannels) GetManifest(ctx context.Context, req *connect.Request[pb.GetManifestRequest]) (*connect.Response[pb.GetManifestResponse], error) {
	reply, err := s.client.GetManifest(prototypeContext(ctx, req.Header()), req.Msg)
	if err != nil {
		return nil, prototypeError(err)
	}
	return connect.NewResponse(reply), nil
}

type connectAuth struct {
	rpc.UnimplementedAuthServiceHandler
	client pb.AuthServiceClient
}

func (s connectAuth) CreateChallenge(ctx context.Context, req *connect.Request[pb.CreateChallengeRequest]) (*connect.Response[pb.CreateChallengeResponse], error) {
	reply, err := s.client.CreateChallenge(prototypeContext(ctx, req.Header()), req.Msg)
	if err != nil {
		return nil, prototypeError(err)
	}
	return connect.NewResponse(reply), nil
}
func (s connectAuth) CompleteChallenge(ctx context.Context, req *connect.Request[pb.CompleteChallengeRequest]) (*connect.Response[pb.CompleteChallengeResponse], error) {
	reply, err := s.client.CompleteChallenge(prototypeContext(ctx, req.Header()), req.Msg)
	if err != nil {
		return nil, prototypeError(err)
	}
	return connect.NewResponse(reply), nil
}

type connectSync struct {
	rpc.UnimplementedSyncServiceHandler
	client pb.SyncServiceClient
}

func (s connectSync) Subscribe(ctx context.Context, req *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.SubscribeResponse]) error {
	ctx, cancel := context.WithCancel(prototypeContext(ctx, req.Header()))
	defer cancel()
	upstream, err := s.client.Subscribe(ctx, req.Msg)
	if err != nil {
		return prototypeError(err)
	}
	for {
		message, err := upstream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return prototypeError(err)
		}
		if err := stream.Send(message); err != nil {
			return err
		}
	}
}

// Маршруты включаются явно флагом -connect-rpc; основной клиент остаётся на gRPC.
func WithConnectPrototype(base http.Handler, connection *grpc.ClientConn) http.Handler {
	options := []connect.HandlerOption{connect.WithReadMaxBytes(16 * 1024), connect.WithSendMaxBytes(1024 * 1024)}
	_, channels := rpc.NewChannelServiceHandler(connectChannels{client: pb.NewChannelServiceClient(connection)}, options...)
	_, auth := rpc.NewAuthServiceHandler(connectAuth{client: pb.NewAuthServiceClient(connection)}, options...)
	_, sync := rpc.NewSyncServiceHandler(connectSync{client: pb.NewSyncServiceClient(connection)}, options...)
	mux := http.NewServeMux()
	mux.Handle(rpc.ChannelServiceGetManifestProcedure, channels)
	mux.Handle(rpc.AuthServiceCreateChallengeProcedure, auth)
	mux.Handle(rpc.AuthServiceCompleteChallengeProcedure, auth)
	mux.Handle(rpc.SyncServiceSubscribeProcedure, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sync.ServeHTTP(connectStreamWriter{w}, r) }))
	mux.Handle("/", base)
	return http.MaxBytesHandler(mux, 16*1024)
}

func isConnectProcedure(path string) bool {
	switch path {
	case rpc.ChannelServiceGetManifestProcedure, rpc.AuthServiceCreateChallengeProcedure, rpc.AuthServiceCompleteChallengeProcedure, rpc.SyncServiceSubscribeProcedure:
		return true
	}
	return false
}

type connectStreamWriter struct{ http.ResponseWriter }

func (w connectStreamWriter) Flush()                      { _ = http.NewResponseController(w.ResponseWriter).Flush() }
func (w connectStreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w connectStreamWriter) Write(data []byte) (int, error) {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(20 * time.Second))
	return w.ResponseWriter.Write(data)
}
