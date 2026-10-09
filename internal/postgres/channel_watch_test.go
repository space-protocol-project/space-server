package postgres

import (
	"context"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"testing"
	"time"
)

func catalogSnapshot(t *testing.T, stream pb.ChannelService_WatchChannelsClient) *pb.WatchChannelsResponse {
	t.Helper()
	for {
		frame, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if !frame.Heartbeat {
			return frame
		}
	}
}

func TestWatchChannelsWithoutReadableChannelAndRevocation(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, false)
	generalList, err := f.channels.ListChannels(f.owner, &pb.ListChannelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.acl(t, generalList.Channels[0])
	secret := f.create(t, "catalog-secret")
	f.acl(t, secret)
	ctx, cancel := context.WithTimeout(member, 8*time.Second)
	defer cancel()
	stream, err := f.channels.WatchChannels(ctx, &pb.WatchChannelsRequest{IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	initial := catalogSnapshot(t, stream)
	if len(initial.Channels) != 0 || initial.Role != "member" || initial.ServerId != f.identity.ServerID {
		t.Fatal(initial)
	}
	// Изменение невидимого канала не должно создавать кадр для этого участника.
	changed, err := f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: secret.Id, Title: "Приватное название", ExpectedRevision: secret.Revision, Position: 20})
	if err != nil {
		t.Fatal(err)
	}
	secret = changed.Channel
	type received struct {
		frame *pb.WatchChannelsResponse
		err   error
	}
	next := make(chan received, 1)
	go func() { frame, err := stream.Recv(); next <- received{frame, err} }()
	select {
	case frame := <-next:
		t.Fatalf("Изменение приватного канала вызвало кадр: %v %v", frame.frame, frame.err)
	case <-time.After(700 * time.Millisecond):
	}
	f.acl(t, secret, principalRule(id, permissions(true, false, false, false)))
	result := <-next
	if result.err != nil {
		t.Fatal(result.err)
	}
	visible := result.frame
	if len(visible.Channels) != 1 || visible.Channels[0].Id != secret.Id || visible.Channels[0].Permissions.Read {
		t.Fatal(visible)
	}
	f.acl(t, secret, principalRule(id, permissions(true, true, true, false)))
	readable := catalogSnapshot(t, stream)
	if len(readable.Channels) != 1 || !readable.Channels[0].Permissions.Write {
		t.Fatal(readable)
	}
	if _, err = f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: id, Role: "reader", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	reader := catalogSnapshot(t, stream)
	if reader.Role != "reader" || reader.Channels[0].Permissions.Write {
		t.Fatal(reader)
	}
	f.acl(t, secret)
	removed := catalogSnapshot(t, stream)
	if len(removed.Channels) != 0 {
		t.Fatal(removed)
	}
	if _, err = f.auth.Logout(member, &pb.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	requireCode(t, err, codes.Unauthenticated)
}
