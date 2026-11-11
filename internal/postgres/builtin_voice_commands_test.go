package postgres

import (
	"github.com/pion/webrtc/v4"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"testing"
)

func TestBuiltinRegistryCommandsVoice(t *testing.T) {
	f := newChannelFixture(t)
	channel, err := f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: "command_voice", Title: "Комната", ViewType: "voice_room"})
	if err != nil {
		t.Fatal(err)
	}
	instance := "builtin:" + channel.Channel.Id + ":voice_room"
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	for i := 0; i < 7; i++ {
		if _, err = peer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
			t.Fatal(err)
		}
	}
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	ready := webrtc.GatheringCompletePromise(peer)
	if err = peer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	member, id := f.enroll(t, false)
	api := pb.NewBuiltinExtensionServiceClient(f.conn)
	result, err := api.ExecuteBuiltinCommand(member, builtinCommand(t, instance, &pb.JoinVoiceRoomRequest{ChannelId: channel.Channel.Id, OfferSdp: peer.LocalDescription().SDP}))
	if err != nil {
		t.Fatal(err)
	}
	var joined pb.JoinVoiceRoomResponse
	if err = result.Result.UnmarshalTo(&joined); err != nil {
		t.Fatal(err)
	}
	if err = peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: joined.AnswerSdp}); err != nil {
		t.Fatal(err)
	}
	legacy := pb.NewVoiceServiceClient(f.conn)
	// Старый RPC видит сессию, созданную через адаптер.
	if _, err = legacy.UpdateVoiceRoom(member, &pb.UpdateVoiceRoomRequest{ChannelId: channel.Channel.Id, SessionId: joined.SessionId, Muted: true}); err != nil {
		t.Fatal(err)
	}
	leave := builtinCommand(t, instance, &pb.LeaveVoiceRoomRequest{ChannelId: channel.Channel.Id, SessionId: joined.SessionId})
	_, err = api.ExecuteBuiltinCommand(f.owner, leave)
	requireCode(t, err, codes.PermissionDenied)
	// Отзыв чтения не должен мешать владельцу освободить сессию.
	f.acl(t, channel.Channel, principalRule(id, permissions(false, false, false, false)))
	if _, err = api.ExecuteBuiltinCommand(member, leave); err != nil {
		t.Fatal(err)
	}
	state, err := legacy.GetVoiceRoom(f.owner, &pb.GetVoiceRoomRequest{ChannelId: channel.Channel.Id})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.State.Participants) != 0 {
		t.Fatal("Голосовая сессия не освобождена")
	}
}
