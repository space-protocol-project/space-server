package postgres

import (
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"testing"
)

func TestVoiceCapacityPersistenceAndValidation(t *testing.T) {
	f := newChannelFixture(t)
	created, err := f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: "capacity", Title: "Голос", ViewType: "voice_room", VoiceCapacity: 32})
	if err != nil {
		t.Fatal(err)
	}
	room := created.Channel
	if room.VoiceCapacity != 32 {
		t.Fatal(room)
	}
	for _, capacity := range []int32{1, 33, -1} {
		_, err = f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: room.Id, Title: room.Title, ExpectedRevision: room.Revision, VoiceCapacity: capacity})
		requireCode(t, err, codes.InvalidArgument)
	}
	updated, err := f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: room.Id, Title: room.Title, ExpectedRevision: room.Revision, VoiceCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	room = updated.Channel
	preserved, err := f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: room.Id, Title: room.Title, ExpectedRevision: room.Revision})
	if err != nil || preserved.Channel.VoiceCapacity != 2 {
		t.Fatal(preserved, err)
	}
	state, err := pb.NewVoiceServiceClient(f.conn).GetVoiceRoom(f.owner, &pb.GetVoiceRoomRequest{ChannelId: room.Id})
	if err != nil || state.State.Capacity != 2 {
		t.Fatal(state, err)
	}
	_, err = f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: room.Id, Title: room.Title, ExpectedRevision: room.Revision, VoiceCapacity: 16})
	requireCode(t, err, codes.Aborted)
	_, err = f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: "bad_capacity", Title: "Чат", ViewType: "chat", VoiceCapacity: 16})
	requireCode(t, err, codes.InvalidArgument)
}
