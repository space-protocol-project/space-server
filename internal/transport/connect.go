package transport

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"io"
	"net/http"
	"time"
)

func bridgeContext(ctx context.Context, header http.Header) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.MD{"authorization": header.Values("Authorization")})
}
func bridgeError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*connect.Error); ok {
		return err
	}
	return connect.NewError(connect.Code(status.Code(err)), status.Convert(err).Err())
}

func bridgeUnary[I, O any](ctx context.Context, req *connect.Request[I], call func(context.Context, *I, ...grpc.CallOption) (*O, error)) (*connect.Response[O], error) {
	reply, err := call(bridgeContext(ctx, req.Header()), req.Msg)
	if err != nil {
		return nil, bridgeError(err)
	}
	return connect.NewResponse(reply), nil
}

func bridgeStream[O any](stream *connect.ServerStream[O], upstream interface{ Recv() (*O, error) }) error {
	var err error
	for err == nil {
		err = forwardMessage(stream, upstream)
	}
	if err == io.EOF {
		return nil
	}
	return bridgeError(err)
}

func forwardMessage[O any](stream *connect.ServerStream[O], upstream interface{ Recv() (*O, error) }) error {
	message, err := upstream.Recv()
	if err != nil {
		return err
	}
	return stream.Send(message)
}

type connectChannel struct {
	rpc.UnimplementedChannelServiceHandler
	client pb.ChannelServiceClient
}

func (s connectChannel) ListChannels(ctx context.Context, req *connect.Request[pb.ListChannelsRequest]) (*connect.Response[pb.ListChannelsResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListChannels)
}

func (s connectChannel) CreateChannel(ctx context.Context, req *connect.Request[pb.CreateChannelRequest]) (*connect.Response[pb.CreateChannelResponse], error) {
	return bridgeUnary(ctx, req, s.client.CreateChannel)
}

func (s connectChannel) UpdateChannel(ctx context.Context, req *connect.Request[pb.UpdateChannelRequest]) (*connect.Response[pb.UpdateChannelResponse], error) {
	return bridgeUnary(ctx, req, s.client.UpdateChannel)
}

func (s connectChannel) DeleteChannel(ctx context.Context, req *connect.Request[pb.DeleteChannelRequest]) (*connect.Response[pb.DeleteChannelResponse], error) {
	return bridgeUnary(ctx, req, s.client.DeleteChannel)
}

func (s connectChannel) GetChannelAccess(ctx context.Context, req *connect.Request[pb.GetChannelAccessRequest]) (*connect.Response[pb.GetChannelAccessResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetChannelAccess)
}

func (s connectChannel) UpdateChannelAccess(ctx context.Context, req *connect.Request[pb.UpdateChannelAccessRequest]) (*connect.Response[pb.UpdateChannelAccessResponse], error) {
	return bridgeUnary(ctx, req, s.client.UpdateChannelAccess)
}

func (s connectChannel) GetManifest(ctx context.Context, req *connect.Request[pb.GetManifestRequest]) (*connect.Response[pb.GetManifestResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetManifest)
}

func (s connectChannel) WatchChannels(ctx context.Context, req *connect.Request[pb.WatchChannelsRequest], stream *connect.ServerStream[pb.WatchChannelsResponse]) error {
	ctx, cancel := context.WithCancel(bridgeContext(ctx, req.Header()))
	defer cancel()
	upstream, err := s.client.WatchChannels(ctx, req.Msg)
	if err != nil {
		return bridgeError(err)
	}
	return bridgeStream(stream, upstream)
}

type connectContent struct {
	rpc.UnimplementedContentServiceHandler
	client pb.ContentServiceClient
}

func (s connectContent) ApplyContentMutation(ctx context.Context, req *connect.Request[pb.ApplyContentMutationRequest]) (*connect.Response[pb.ApplyContentMutationResponse], error) {
	return bridgeUnary(ctx, req, s.client.ApplyContentMutation)
}

func (s connectContent) CreateContent(ctx context.Context, req *connect.Request[pb.CreateContentRequest]) (*connect.Response[pb.CreateContentResponse], error) {
	return bridgeUnary(ctx, req, s.client.CreateContent)
}

func (s connectContent) SetReaction(ctx context.Context, req *connect.Request[pb.SetReactionRequest]) (*connect.Response[pb.SetReactionResponse], error) {
	return bridgeUnary(ctx, req, s.client.SetReaction)
}
func (s connectContent) UpdateContent(ctx context.Context, req *connect.Request[pb.UpdateContentRequest]) (*connect.Response[pb.UpdateContentResponse], error) {
	return bridgeUnary(ctx, req, s.client.UpdateContent)
}
func (s connectContent) DeleteContent(ctx context.Context, req *connect.Request[pb.DeleteContentRequest]) (*connect.Response[pb.DeleteContentResponse], error) {
	return bridgeUnary(ctx, req, s.client.DeleteContent)
}

func (s connectContent) GetHistoryPage(ctx context.Context, req *connect.Request[pb.GetHistoryPageRequest]) (*connect.Response[pb.GetHistoryPageResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetHistoryPage)
}

func (s connectContent) SearchContent(ctx context.Context, req *connect.Request[pb.SearchContentRequest]) (*connect.Response[pb.SearchContentResponse], error) {
	return bridgeUnary(ctx, req, s.client.SearchContent)
}

func (s connectContent) ListContent(ctx context.Context, req *connect.Request[pb.ListContentRequest]) (*connect.Response[pb.ListContentResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListContent)
}

type connectAuth struct {
	rpc.UnimplementedAuthServiceHandler
	client pb.AuthServiceClient
}

func (s connectAuth) CreateRootRotation(ctx context.Context, req *connect.Request[pb.CreateRootRotationRequest]) (*connect.Response[pb.CreateRootRotationResponse], error) {
	return bridgeUnary(ctx, req, s.client.CreateRootRotation)
}

func (s connectAuth) CompleteRootRotation(ctx context.Context, req *connect.Request[pb.CompleteRootRotationRequest]) (*connect.Response[pb.CompleteRootRotationResponse], error) {
	return bridgeUnary(ctx, req, s.client.CompleteRootRotation)
}

func (s connectAuth) ProposePairing(ctx context.Context, req *connect.Request[pb.ProposePairingRequest]) (*connect.Response[pb.ProposePairingResponse], error) {
	return bridgeUnary(ctx, req, s.client.ProposePairing)
}

func (s connectAuth) CreatePairing(ctx context.Context, req *connect.Request[pb.CreatePairingRequest]) (*connect.Response[pb.CreatePairingResponse], error) {
	return bridgeUnary(ctx, req, s.client.CreatePairing)
}

func (s connectAuth) InspectPairing(ctx context.Context, req *connect.Request[pb.InspectPairingRequest]) (*connect.Response[pb.InspectPairingResponse], error) {
	return bridgeUnary(ctx, req, s.client.InspectPairing)
}

func (s connectAuth) PollPairing(ctx context.Context, req *connect.Request[pb.PollPairingRequest]) (*connect.Response[pb.PollPairingResponse], error) {
	return bridgeUnary(ctx, req, s.client.PollPairing)
}

func (s connectAuth) CancelPairing(ctx context.Context, req *connect.Request[pb.CancelPairingRequest]) (*connect.Response[pb.CancelPairingResponse], error) {
	return bridgeUnary(ctx, req, s.client.CancelPairing)
}

func (s connectAuth) ClaimPairing(ctx context.Context, req *connect.Request[pb.ClaimPairingRequest]) (*connect.Response[pb.ClaimPairingResponse], error) {
	return bridgeUnary(ctx, req, s.client.ClaimPairing)
}

func (s connectAuth) RevokeCurrentDevice(ctx context.Context, req *connect.Request[pb.RevokeCurrentDeviceRequest]) (*connect.Response[pb.RevokeCurrentDeviceResponse], error) {
	return bridgeUnary(ctx, req, s.client.RevokeCurrentDevice)
}

func (s connectAuth) ListDevices(ctx context.Context, req *connect.Request[pb.ListDevicesRequest]) (*connect.Response[pb.ListDevicesResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListDevices)
}

func (s connectAuth) Logout(ctx context.Context, req *connect.Request[pb.LogoutRequest]) (*connect.Response[pb.LogoutResponse], error) {
	return bridgeUnary(ctx, req, s.client.Logout)
}

func (s connectAuth) CreateChallenge(ctx context.Context, req *connect.Request[pb.CreateChallengeRequest]) (*connect.Response[pb.CreateChallengeResponse], error) {
	return bridgeUnary(ctx, req, s.client.CreateChallenge)
}

func (s connectAuth) CompleteChallenge(ctx context.Context, req *connect.Request[pb.CompleteChallengeRequest]) (*connect.Response[pb.CompleteChallengeResponse], error) {
	return bridgeUnary(ctx, req, s.client.CompleteChallenge)
}

type connectSync struct {
	rpc.UnimplementedSyncServiceHandler
	client pb.SyncServiceClient
}

func (s connectSync) ListEvents(ctx context.Context, req *connect.Request[pb.ListEventsRequest]) (*connect.Response[pb.ListEventsResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListEvents)
}

func (s connectSync) Subscribe(ctx context.Context, req *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.SubscribeResponse]) error {
	ctx, cancel := context.WithCancel(bridgeContext(ctx, req.Header()))
	defer cancel()
	upstream, err := s.client.Subscribe(ctx, req.Msg)
	if err != nil {
		return bridgeError(err)
	}
	return bridgeStream(stream, upstream)
}

type connectAdmin struct {
	rpc.UnimplementedAdminServiceHandler
	client pb.AdminServiceClient
}

func (s connectAdmin) ListMembers(ctx context.Context, req *connect.Request[pb.ListMembersRequest]) (*connect.Response[pb.ListMembersResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListMembers)
}

func (s connectAdmin) UpdateMember(ctx context.Context, req *connect.Request[pb.UpdateMemberRequest]) (*connect.Response[pb.UpdateMemberResponse], error) {
	return bridgeUnary(ctx, req, s.client.UpdateMember)
}

func (s connectAdmin) CreateInvite(ctx context.Context, req *connect.Request[pb.CreateInviteRequest]) (*connect.Response[pb.CreateInviteResponse], error) {
	return bridgeUnary(ctx, req, s.client.CreateInvite)
}

func (s connectAdmin) ListInvites(ctx context.Context, req *connect.Request[pb.ListInvitesRequest]) (*connect.Response[pb.ListInvitesResponse], error) {
	return bridgeUnary(ctx, req, s.client.ListInvites)
}

func (s connectAdmin) RevokeInvite(ctx context.Context, req *connect.Request[pb.RevokeInviteRequest]) (*connect.Response[pb.RevokeInviteResponse], error) {
	return bridgeUnary(ctx, req, s.client.RevokeInvite)
}

func (s connectAdmin) GetSetupStatus(ctx context.Context, req *connect.Request[pb.GetSetupStatusRequest]) (*connect.Response[pb.GetSetupStatusResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetSetupStatus)
}

func (s connectAdmin) ClaimOwner(ctx context.Context, req *connect.Request[pb.ClaimOwnerRequest]) (*connect.Response[pb.ClaimOwnerResponse], error) {
	return bridgeUnary(ctx, req, s.client.ClaimOwner)
}

func (s connectAdmin) GetSettings(ctx context.Context, req *connect.Request[pb.GetSettingsRequest]) (*connect.Response[pb.GetSettingsResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetSettings)
}

func (s connectAdmin) UpdateSettings(ctx context.Context, req *connect.Request[pb.UpdateSettingsRequest]) (*connect.Response[pb.UpdateSettingsResponse], error) {
	return bridgeUnary(ctx, req, s.client.UpdateSettings)
}

type connectMembership struct {
	rpc.UnimplementedMembershipServiceHandler
	client pb.MembershipServiceClient
}

func (s connectMembership) PreviewInvite(ctx context.Context, req *connect.Request[pb.PreviewInviteRequest]) (*connect.Response[pb.PreviewInviteResponse], error) {
	return bridgeUnary(ctx, req, s.client.PreviewInvite)
}

func (s connectMembership) AcceptInvite(ctx context.Context, req *connect.Request[pb.AcceptInviteRequest]) (*connect.Response[pb.AcceptInviteResponse], error) {
	return bridgeUnary(ctx, req, s.client.AcceptInvite)
}

func (s connectMembership) UpdateProfile(ctx context.Context, req *connect.Request[pb.UpdateProfileRequest]) (*connect.Response[pb.UpdateProfileResponse], error) {
	return bridgeUnary(ctx, req, s.client.UpdateProfile)
}

func (s connectMembership) GetMembership(ctx context.Context, req *connect.Request[pb.GetMembershipRequest]) (*connect.Response[pb.GetMembershipResponse], error) {
	return bridgeUnary(ctx, req, s.client.GetMembership)
}

// gRPC-мост сохраняет общую авторизацию, ACL и бизнес-логику старых клиентов.
func WithConnect(base http.Handler, connection *grpc.ClientConn, verifier authn.Verifier) http.Handler {
	options := []connect.HandlerOption{connect.WithReadMaxBytes(16 * 1024), connect.WithSendMaxBytes(1024 * 1024)}
	mux := http.NewServeMux()
	pathChannelService, handlerChannelService := rpc.NewChannelServiceHandler(connectChannel{client: pb.NewChannelServiceClient(connection)}, options...)
	mux.Handle(pathChannelService, handlerChannelService)
	pathContentService, handlerContentService := rpc.NewContentServiceHandler(connectContent{client: pb.NewContentServiceClient(connection)}, options...)
	mux.Handle(pathContentService, handlerContentService)
	pathAuthService, handlerAuthService := rpc.NewAuthServiceHandler(connectAuth{client: pb.NewAuthServiceClient(connection)}, options...)
	mux.Handle(pathAuthService, handlerAuthService)
	pathSyncService, handlerSyncService := rpc.NewSyncServiceHandler(connectSync{client: pb.NewSyncServiceClient(connection)}, options...)
	mux.Handle(pathSyncService, handlerSyncService)
	pathAdminService, handlerAdminService := rpc.NewAdminServiceHandler(connectAdmin{client: pb.NewAdminServiceClient(connection)}, options...)
	mux.Handle(pathAdminService, handlerAdminService)
	pathMembershipService, handlerMembershipService := rpc.NewMembershipServiceHandler(connectMembership{client: pb.NewMembershipServiceClient(connection)}, options...)
	mux.Handle(pathMembershipService, handlerMembershipService)
	mlsPath, mlsHandler := rpc.NewMlsServiceHandler(connectMls{client: pb.NewMlsServiceClient(connection)}, connect.WithReadMaxBytes(1024*1024), connect.WithSendMaxBytes(8*1024*1024))
	mux.Handle(mlsPath, mlsHandler)
	assetPath, assetHandler := rpc.NewAssetServiceHandler(connectAsset{client: pb.NewAssetServiceClient(connection)}, connect.WithReadMaxBytes(384*1024), connect.WithSendMaxBytes(1024*1024))
	mux.Handle(assetPath, assetHandler)
	mux.Handle("/", base)
	return connectBodyLimit(connectAuthorization(mux, verifier))
}

func connectAuthorization(next http.Handler, verifier authn.Verifier) http.Handler {
	writer := connect.NewErrorWriter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := authorizeConnectRequest(r, verifier); err != nil {
			_ = writer.Write(w, r, err)
			return
		}
		next.ServeHTTP(connectStreamWriter{w}, r)
	})
}

func authorizeConnectRequest(r *http.Request, verifier authn.Verifier) error {
	if verifier == nil || !isConnectProcedure(r.URL.Path) {
		return nil
	}
	ctx := metadata.NewIncomingContext(r.Context(), metadata.MD{"authorization": r.Header.Values("Authorization")})
	_, err := authn.AuthorizeCall(ctx, verifier, r.URL.Path)
	return bridgeError(err)
}

var connectProcedures = map[string]bool{
	rpc.ContentServiceApplyContentMutationProcedure: true,
	rpc.MlsServiceRegisterMlsDeviceProcedure:        true,
	rpc.MlsServiceListMlsDevicesProcedure:           true,
	rpc.MlsServiceGetMlsChannelProcedure:            true,
	rpc.MlsServicePublishMlsProcedure:               true,
	rpc.MlsServiceListMlsEnvelopesProcedure:         true,
	rpc.MembershipServiceUpdateProfileProcedure:     true,
	rpc.AssetServiceBeginUploadProcedure:            true,
	rpc.AssetServiceUploadChunkProcedure:            true,
	rpc.AssetServiceCompleteUploadProcedure:         true,
	rpc.AssetServiceCancelUploadProcedure:           true,
	rpc.AssetServiceDownloadChunkProcedure:          true,
	rpc.AssetServiceListMyAssetsProcedure:           true,
	rpc.AssetServiceListChannelAssetsProcedure:      true,

	rpc.ChannelServiceListChannelsProcedure:        true,
	rpc.ChannelServiceCreateChannelProcedure:       true,
	rpc.ChannelServiceUpdateChannelProcedure:       true,
	rpc.ChannelServiceDeleteChannelProcedure:       true,
	rpc.ChannelServiceGetChannelAccessProcedure:    true,
	rpc.ChannelServiceUpdateChannelAccessProcedure: true,
	rpc.ChannelServiceGetManifestProcedure:         true,
	rpc.ChannelServiceWatchChannelsProcedure:       true,
	rpc.ContentServiceSetReactionProcedure:         true,
	rpc.ContentServiceUpdateContentProcedure:       true,
	rpc.ContentServiceDeleteContentProcedure:       true,
	rpc.ContentServiceCreateContentProcedure:       true,
	rpc.ContentServiceGetHistoryPageProcedure:      true,
	rpc.ContentServiceSearchContentProcedure:       true,
	rpc.ContentServiceListContentProcedure:         true,
	rpc.AuthServiceCreateRootRotationProcedure:     true,
	rpc.AuthServiceCompleteRootRotationProcedure:   true,
	rpc.AuthServiceProposePairingProcedure:         true,
	rpc.AuthServiceCreatePairingProcedure:          true,
	rpc.AuthServiceInspectPairingProcedure:         true,
	rpc.AuthServicePollPairingProcedure:            true,
	rpc.AuthServiceCancelPairingProcedure:          true,
	rpc.AuthServiceClaimPairingProcedure:           true,
	rpc.AuthServiceRevokeCurrentDeviceProcedure:    true,
	rpc.AuthServiceListDevicesProcedure:            true,
	rpc.AuthServiceLogoutProcedure:                 true,
	rpc.AuthServiceCreateChallengeProcedure:        true,
	rpc.AuthServiceCompleteChallengeProcedure:      true,
	rpc.SyncServiceListEventsProcedure:             true,
	rpc.SyncServiceSubscribeProcedure:              true,
	rpc.AdminServiceListMembersProcedure:           true,
	rpc.AdminServiceUpdateMemberProcedure:          true,
	rpc.AdminServiceCreateInviteProcedure:          true,
	rpc.AdminServiceListInvitesProcedure:           true,
	rpc.AdminServiceRevokeInviteProcedure:          true,
	rpc.AdminServiceGetSetupStatusProcedure:        true,
	rpc.AdminServiceClaimOwnerProcedure:            true,
	rpc.AdminServiceGetSettingsProcedure:           true,
	rpc.AdminServiceUpdateSettingsProcedure:        true,
	rpc.MembershipServicePreviewInviteProcedure:    true,
	rpc.MembershipServiceAcceptInviteProcedure:     true,
	rpc.MembershipServiceGetMembershipProcedure:    true,
}

func isConnectProcedure(path string) bool { return connectProcedures[path] }

type connectStreamWriter struct{ http.ResponseWriter }

func (w connectStreamWriter) Flush()                      { _ = http.NewResponseController(w.ResponseWriter).Flush() }
func (w connectStreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w connectStreamWriter) Write(data []byte) (int, error) {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(20 * time.Second))
	return w.ResponseWriter.Write(data)
}
