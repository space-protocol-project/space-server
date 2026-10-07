package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"google.golang.org/grpc"
)

func Handler(ctx context.Context, connection *grpc.ClientConn, serverID string, publicKey ...[]byte) (http.Handler, error) {
	// Authorization прокидывается runtime отдельно; клиентская Grpc-Metadata-* не принимается.
	gateway := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(func(string) (string, bool) { return "", false }))
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
	mux := http.NewServeMux()
	mux.Handle("/api/", gateway)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /.well-known/space-protocol", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		discovery := map[string]string{"protocol_version": "0.1-experimental", "server_id": serverID, "manifest": "/api/v1/manifest"}
		if len(publicKey) > 0 && len(publicKey[0]) > 0 {
			discovery["signing_algorithm"] = "Ed25519"
			discovery["signing_public_key"] = base64.RawURLEncoding.EncodeToString(publicKey[0])
		}
		_ = json.NewEncoder(w).Encode(discovery)
	})
	return http.MaxBytesHandler(mux, 16*1024), nil
}
