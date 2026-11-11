package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"net/http"
)

// NewExtensionP1Handler явно подключает экспериментальный API к доверенному
// gRPC-сервису с interceptor ядра. В рабочем сервере автоматически не включается.
func NewExtensionP1Handler(client pb.ExtensionP1ServiceClient) (string, http.Handler) {
	return rpc.NewExtensionP1ServiceHandler(connectExtensionP1{client: client}, connect.WithReadMaxBytes(20*1024))
}

type connectExtensionP1 struct{ client pb.ExtensionP1ServiceClient }

func (s connectExtensionP1) ListExtensionContributions(ctx context.Context, req *connect.Request[pb.ListExtensionContributionsRequest]) (*connect.Response[pb.ListExtensionContributionsResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListExtensionContributions)
}

func (s connectExtensionP1) QueryExtension(ctx context.Context, req *connect.Request[pb.QueryExtensionRequest]) (*connect.Response[pb.QueryExtensionResponse], error) {
	return bridgeUnary(ctx, req, s.client.QueryExtension)
}

func (s connectExtensionP1) ExecuteExtensionCommand(ctx context.Context, req *connect.Request[pb.ExecuteExtensionCommandRequest]) (*connect.Response[pb.ExecuteExtensionCommandResponse], error) {
	return bridgeUnary(ctx, req, s.client.ExecuteExtensionCommand)
}

func (s connectExtensionP1) ReconcileExtensionCommand(ctx context.Context, req *connect.Request[pb.ReconcileExtensionCommandRequest]) (*connect.Response[pb.ReconcileExtensionCommandResponse], error) {
	return bridgeUnary(ctx, req, s.client.ReconcileExtensionCommand)
}

func (s connectExtensionP1) GetExtensionMutationContext(ctx context.Context, req *connect.Request[pb.GetExtensionMutationContextRequest]) (*connect.Response[pb.GetExtensionMutationContextResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetExtensionMutationContext)
}

func (s connectExtensionP1) ListExtensionInstallations(ctx context.Context, req *connect.Request[pb.ListExtensionInstallationsRequest]) (*connect.Response[pb.ListExtensionInstallationsResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListExtensionInstallations)
}

func (s connectExtensionP1) GetExtensionInstallation(ctx context.Context, req *connect.Request[pb.GetExtensionInstallationRequest]) (*connect.Response[pb.GetExtensionInstallationResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetExtensionInstallation)
}

func (s connectExtensionP1) ChangeExtensionInstallation(ctx context.Context, req *connect.Request[pb.ChangeExtensionInstallationRequest]) (*connect.Response[pb.ChangeExtensionInstallationResponse], error) {
	return bridgeUnary(ctx, req, s.client.ChangeExtensionInstallation)
}

func (s connectExtensionP1) ListExtensionInstallationAudit(ctx context.Context, req *connect.Request[pb.ListExtensionInstallationAuditRequest]) (*connect.Response[pb.ListExtensionInstallationAuditResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListExtensionInstallationAudit)
}
