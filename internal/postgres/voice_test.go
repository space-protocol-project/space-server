package postgres

import (
	"bytes"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func TestVoiceRoomChannelPermissionsAndPlaintextIsolation(t *testing.T) {
	f := newChannelFixture(t)
	if _, err := pb.NewMembershipServiceClient(f.conn).UpdateProfile(f.owner, &pb.UpdateProfileRequest{Nickname: "Алиса"}); err != nil {
		t.Fatal(err)
	}
	verified, err := authn.AuthorizeCall(metadata.NewIncomingContext(f.ctx, metadata.Pairs("authorization", "Bearer "+fixtureToken(f.owner))), f.store, "/space.v1.VoiceService/GetVoiceRoom")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: "voice", Title: "Голос", ViewType: "voice_room"})
	if err != nil {
		t.Fatal(err)
	}
	room := reply.Channel
	identity, err := f.store.VoiceIdentity(verified, room.Id)
	if err != nil || identity.Name != "Алиса" {
		t.Fatal("Имя владельца потеряно", identity, err)
	}
	if room.Views[0].Type != "voice_room" {
		t.Fatal(room)
	}
	client := pb.NewVoiceServiceClient(f.conn)
	state, err := client.GetVoiceRoom(f.owner, &pb.GetVoiceRoomRequest{ChannelId: room.Id})
	if err != nil || !state.GetState().GetCanPublish() || state.State.Capacity != 2 {
		t.Fatal(state, err)
	}
	member, id := f.enroll(t, false)
	if _, err = client.GetVoiceRoom(member, &pb.GetVoiceRoomRequest{ChannelId: room.Id}); err != nil {
		t.Fatal(err)
	}
	f.acl(t, room, principalRule(id, permissions(true, true, false, false)))
	state, err = client.GetVoiceRoom(member, &pb.GetVoiceRoomRequest{ChannelId: room.Id})
	if err != nil || state.State.CanPublish {
		t.Fatal("право говорить не отделено", state, err)
	}
	f.acl(t, room, principalRule(id, permissions(false, false, false, false)))
	_, err = client.GetVoiceRoom(member, &pb.GetVoiceRoomRequest{ChannelId: room.Id})
	requireCode(t, err, codes.NotFound)
	_, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: room.Id, Text: "Текст в голосе", IdempotencyKey: "voice-text"})
	requireCode(t, err, codes.FailedPrecondition)
	_, err = f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: "voice_public", Title: "Голос", ViewType: "voice_room", PublicPreview: true})
	requireCode(t, err, codes.InvalidArgument)
	_, err = f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: "voice_mls", Title: "Голос", ViewType: "voice_room", MlsGroupId: bytes.Repeat([]byte{1}, 32)})
	requireCode(t, err, codes.InvalidArgument)
	_, err = f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: room.Id, Title: room.Title, Archived: true, ExpectedRevision: room.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetVoiceRoom(f.owner, &pb.GetVoiceRoomRequest{ChannelId: room.Id})
	requireCode(t, err, codes.FailedPrecondition)
}
