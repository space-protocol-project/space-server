package postgres

import (
	"bytes"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/builtin"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func TestBuiltinRegistryMLS(t *testing.T) {
	f := newChannelFixture(t)
	alice, _ := enrollMls(t, f, true)
	bob, device := enrollMls(t, f, false)
	outsider, _ := enrollMls(t, f, false)
	c, err := f.channels.CreateChannel(alice, &pb.CreateChannelRequest{ChannelId: "builtin_mls", Title: "MLS", ViewType: "chat", MlsGroupId: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	api := pb.NewMlsServiceClient(f.conn)
	_, err = api.PublishMls(alice, &pb.PublishMlsRequest{ChannelId: c.Channel.Id, RequestId: bytes.Repeat([]byte{1}, 16), Kind: "commit", Ciphertext: []byte("opaque-commit"), AddGrantId: device.GrantId, Welcome: []byte("recipient-only")})
	if err != nil {
		t.Fatal(err)
	}

	command := &pb.PublishMlsRequest{ChannelId: c.Channel.Id, RequestId: bytes.Repeat([]byte{2}, 16), ExpectedEpoch: 1, Kind: "application", Ciphertext: []byte("opaque-message")}
	commands := pb.NewBuiltinExtensionServiceClient(f.conn)
	request := builtinCommand(t, "builtin:"+c.Channel.Id+":chat", command)
	result, err := commands.ExecuteBuiltinCommand(alice, request)
	if err != nil {
		t.Fatal(err)
	}
	old, err := api.PublishMls(alice, command)
	if err != nil {
		t.Fatal(err)
	}
	var published pb.PublishMlsResponse
	if err = result.Result.UnmarshalTo(&published); err != nil || !proto.Equal(&published, old) {
		t.Fatalf("Нарушен повтор MLS: %v", err)
	}
	_, err = commands.ExecuteBuiltinCommand(outsider, request)
	requireCode(t, err, codes.PermissionDenied)
	registry := builtin.New(f.store, nil)
	instance := "builtin:" + c.Channel.Id + ":chat"
	for _, actor := range []struct {
		source      string
		wantWelcome bool
	}{{"alice", false}, {"bob", true}} {
		source := alice
		if actor.source == "bob" {
			source = bob
		}
		expected, err := api.ListMlsEnvelopes(source, &pb.ListMlsEnvelopesRequest{ChannelId: c.Channel.Id})
		if err != nil {
			t.Fatal(err)
		}
		result, err := registry.Open(builtinContext(t, f, source), instance)
		if err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(result.Content, expected) || len(expected.Envelopes) != 2 {
			t.Fatal("Нарушен паритет конвертов")
		}
		if (len(expected.Envelopes[0].Welcome) > 0) != actor.wantWelcome {
			t.Fatal("Welcome раскрыт чужому устройству")
		}
	}
	_, err = registry.Open(builtinContext(t, f, outsider), instance)
	requireCode(t, err, codes.PermissionDenied)
}
