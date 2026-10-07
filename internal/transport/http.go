package transport

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"google.golang.org/grpc"
)

func Handler(ctx context.Context, connection *grpc.ClientConn, serverID string) (http.Handler, error) {
	gateway := runtime.NewServeMux()
	if err := pb.RegisterChannelServiceHandler(ctx, gateway, connection); err != nil {
		return nil, err
	}
	if err := pb.RegisterContentServiceHandler(ctx, gateway, connection); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", gateway)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /.well-known/space-protocol", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"protocol_version": "0.1-experimental", "server_id": serverID, "manifest": "/api/v1/manifest"})
	})
	return http.MaxBytesHandler(mux, 16*1024), nil
}
