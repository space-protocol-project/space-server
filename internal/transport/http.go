package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/spaceweb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func Handler(ctx context.Context, connection *grpc.ClientConn, serverID string, publicKey ...[]byte) (http.Handler, error) {
	var key []byte
	if len(publicKey) > 0 {
		key = publicKey[0]
	}
	return HandlerWithEndpoint(ctx, connection, serverID, key, "")
}

func HandlerWithEndpoint(ctx context.Context, connection *grpc.ClientConn, serverID string, publicKey []byte, endpoint string) (http.Handler, error) {
	// Authorization прокидывается runtime отдельно; клиентская Grpc-Metadata-* не принимается.
	gateway := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(func(string) (string, bool) { return "", false }), runtime.WithForwardResponseOption(func(_ context.Context, w http.ResponseWriter, message proto.Message) error {
		if _, ok := message.(*pb.SubscribeResponse); !ok {
			return nil
		}
		err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(20 * time.Second))
		if errors.Is(err, http.ErrNotSupported) {
			return nil
		}
		return err
	}))
	if err := pb.RegisterChannelServiceHandler(ctx, gateway, connection); err != nil {
		return nil, err
	}
	if err := pb.RegisterContentServiceHandler(ctx, gateway, connection); err != nil {
		return nil, err
	}
	if err := pb.RegisterAuthServiceHandler(ctx, gateway, connection); err != nil {
		return nil, err
	}
	if err := pb.RegisterSyncServiceHandler(ctx, gateway, connection); err != nil {
		return nil, err
	}
	if err := pb.RegisterAdminServiceHandler(ctx, gateway, connection); err != nil {
		return nil, err
	}
	if err := pb.RegisterMembershipServiceHandler(ctx, gateway, connection); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", gateway)
	if directory := os.Getenv("SPACE_ADMIN_WEB_DIR"); directory != "" {
		panel, err := spaceweb.FlutterHandler(directory)
		if err != nil {
			return nil, err
		}
		mux.Handle("/space/flutter/", panel)
	}
	mux.Handle("/space", spaceweb.Handler())
	mux.Handle("/space/", spaceweb.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /.well-known/space-protocol", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		discovery := map[string]string{"protocol_version": "0.1-experimental", "server_id": serverID, "manifest": "/api/v1/manifest"}
		if len(publicKey) > 0 {
			discovery["signing_algorithm"] = "Ed25519"
			discovery["signing_public_key"] = base64.RawURLEncoding.EncodeToString(publicKey)
		}
		if endpoint != "" {
			discovery["grpc_endpoint"] = endpoint
		}
		_ = json.NewEncoder(w).Encode(discovery)
	})
	return http.MaxBytesHandler(mux, 16*1024), nil
}
