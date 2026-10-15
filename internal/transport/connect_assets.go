package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"net/http"
)

type connectAsset struct {
	rpc.UnimplementedAssetServiceHandler
	client pb.AssetServiceClient
}

func (s connectAsset) BeginUpload(ctx context.Context, req *connect.Request[pb.BeginUploadRequest]) (*connect.Response[pb.BeginUploadResponse], error) {
	return bridgeUnary(ctx, req, s.client.BeginUpload)
}
func (s connectAsset) UploadChunk(ctx context.Context, req *connect.Request[pb.UploadChunkRequest]) (*connect.Response[pb.UploadChunkResponse], error) {
	return bridgeUnary(ctx, req, s.client.UploadChunk)
}
func (s connectAsset) CompleteUpload(ctx context.Context, req *connect.Request[pb.CompleteUploadRequest]) (*connect.Response[pb.CompleteUploadResponse], error) {
	return bridgeUnary(ctx, req, s.client.CompleteUpload)
}
func (s connectAsset) CancelUpload(ctx context.Context, req *connect.Request[pb.CancelUploadRequest]) (*connect.Response[pb.CancelUploadResponse], error) {
	return bridgeUnary(ctx, req, s.client.CancelUpload)
}
func (s connectAsset) DownloadChunk(ctx context.Context, req *connect.Request[pb.DownloadChunkRequest]) (*connect.Response[pb.DownloadChunkResponse], error) {
	return bridgeUnary(ctx, req, s.client.DownloadChunk)
}
func (s connectAsset) ListMyAssets(ctx context.Context, req *connect.Request[pb.ListMyAssetsRequest]) (*connect.Response[pb.ListMyAssetsResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListMyAssets)
}

func connectBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(requestMessageLimit(r.URL.Path))
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
