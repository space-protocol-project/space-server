package postgres

import (
	"bytes"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

func TestMlsReaderSelfUpdatePreservesRosterAndManager(t *testing.T) {
	f := newChannelFixture(t)
	alice, a := enrollMls(t, f, true)
	bob, b := enrollMls(t, f, false)
	rpc := pb.NewMlsServiceClient(f.conn)
	_, err := f.channels.CreateChannel(alice, &pb.CreateChannelRequest{ChannelId: "updates", Title: "Закрытый", ViewType: "chat", MlsGroupId: bytes.Repeat([]byte{5}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = rpc.PublishMls(alice, &pb.PublishMlsRequest{ChannelId: "updates", RequestId: bytes.Repeat([]byte{1}, 16), Kind: "commit", Ciphertext: []byte("add"), AddGrantId: b.GrantId, Welcome: []byte("welcome")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: b.PrincipalId, Role: "reader", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	request := &pb.PublishMlsRequest{ChannelId: "updates", RequestId: bytes.Repeat([]byte{2}, 16), ExpectedEpoch: 1, Kind: "update", Ciphertext: []byte("self-update")}
	for range 2 {
		response, err := rpc.PublishMls(bob, request)
		if err != nil {
			t.Fatal(err)
		}
		if response.Envelope.Sequence != 2 || len(response.Envelope.Roster) != 2 {
			t.Fatal("Self-update изменил состав или повтор создал событие")
		}
	}
	var manager string
	var epoch int64
	err = f.store.pool.QueryRow(f.ctx, "SELECT manager_grant_id,epoch FROM mls_channels WHERE channel_id='updates'").Scan(&manager, &epoch)
	if err != nil || manager != a.GrantId || epoch != 2 {
		t.Fatalf("Нарушены управление/эпоха: %s %d %v", manager, epoch, err)
	}
	request.AddGrantId = a.GrantId
	_, err = rpc.PublishMls(bob, request)
	requireCode(t, err, codes.InvalidArgument)
}
