package postgres

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func builtinCommand(t *testing.T, instance string, command proto.Message) *pb.ExecuteBuiltinCommandRequest {
	t.Helper()
	payload, err := anypb.New(command)
	if err != nil {
		t.Fatal(err)
	}
	return &pb.ExecuteBuiltinCommandRequest{InstanceId: instance, Command: payload}
}

func TestBuiltinRegistryCommandsRPC(t *testing.T) {
	f := newChannelFixture(t)
	channel := f.create(t, "commands_chat")
	instance := "builtin:" + channel.Id + ":chat"
	api := pb.NewBuiltinExtensionServiceClient(f.conn)
	command := &pb.CreateContentRequest{ChannelId: channel.Id, Text: "Единая запись", IdempotencyKey: "builtin-command"}
	request := builtinCommand(t, instance, command)
	first, err := api.ExecuteBuiltinCommand(f.owner, request)
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.contents.CreateContent(f.owner, command)
	if err != nil {
		t.Fatal(err)
	}
	var created pb.CreateContentResponse
	if err = first.Result.UnmarshalTo(&created); err != nil || !proto.Equal(&created, old) {
		t.Fatalf("Повтор создал другую запись: %v", err)
	}
	// Настоящий HTTP Connect -> gRPC -> те же службы и база.
	server := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer server.Close()
	client := rpc.NewBuiltinExtensionServiceClient(server.Client(), server.URL)
	wire := connect.NewRequest(request)
	wire.Header().Set("Authorization", outgoingAuthorization(f.owner))
	again, err := client.ExecuteBuiltinCommand(f.ctx, wire)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(first, again.Msg) {
		t.Fatal("Connect изменил квитанцию")
	}
	opened, err := api.OpenBuiltinSection(f.owner, &pb.OpenBuiltinSectionRequest{InstanceId: instance})
	if err != nil {
		t.Fatal(err)
	}
	var page pb.ListContentResponse
	if err = opened.Content.UnmarshalTo(&page); err != nil || len(page.Contents) != 1 {
		t.Fatalf("Нарушен единый путь записи: %v", err)
	}
	_, err = api.ExecuteBuiltinCommand(context.Background(), request)
	requireCode(t, err, codes.Unauthenticated)
	_, err = client.ExecuteBuiltinCommand(f.ctx, connect.NewRequest(request))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal(err)
	}
	other := f.create(t, "commands_other")
	command.ChannelId = other.Id
	_, err = api.ExecuteBuiltinCommand(f.owner, builtinCommand(t, instance, command))
	requireCode(t, err, codes.InvalidArgument)
	_, err = api.ExecuteBuiltinCommand(f.owner, builtinCommand(t, instance, &pb.CreateFeedPostRequest{ChannelId: channel.Id}))
	requireCode(t, err, codes.InvalidArgument)
	_, err = api.ExecuteBuiltinCommand(f.owner, &pb.ExecuteBuiltinCommandRequest{InstanceId: instance})
	requireCode(t, err, codes.InvalidArgument)
	command.ChannelId = channel.Id
	command.Text = strings.Repeat("x", 17000)
	_, err = api.ExecuteBuiltinCommand(f.owner, builtinCommand(t, instance, command))
	requireCode(t, err, codes.ResourceExhausted)
	member, id := f.enroll(t, false)
	f.acl(t, channel, principalRule(id, permissions(true, false, false, false)))
	_, err = api.ExecuteBuiltinCommand(member, request)
	requireCode(t, err, codes.PermissionDenied)
	f.acl(t, channel, principalRule(id, permissions(false, false, false, false)))
	_, err = api.ExecuteBuiltinCommand(member, request)
	requireCode(t, err, codes.NotFound)
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	_, err = api.ExecuteBuiltinCommand(member, request)
	requireCode(t, err, codes.Unauthenticated)
}

func TestBuiltinRegistryCommandsCommunity(t *testing.T) {
	f := newChannelFixture(t)
	api := pb.NewBuiltinExtensionServiceClient(f.conn)
	forum := forumSection(t, f, "command_forum")
	feed := feedSection(t, f, "command_feed")
	topic := &pb.CreateForumThreadRequest{ChannelId: forum.Id, Title: "Тема", Text: "Текст", IdempotencyKey: "topic"}
	post := &pb.CreateFeedPostRequest{ChannelId: feed.Id, Text: "Новость", IdempotencyKey: "post"}
	oldTopic, err := pb.NewForumServiceClient(f.conn).CreateForumThread(f.owner, topic)
	if err != nil {
		t.Fatal(err)
	}
	oldPost, err := pb.NewFeedServiceClient(f.conn).CreateFeedPost(f.owner, post)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		instance        string
		input, expected proto.Message
	}{
		{"builtin:" + forum.Id + ":forum", topic, oldTopic},
		{"builtin:" + feed.Id + ":feed", post, oldPost},
	} {
		result, err := api.ExecuteBuiltinCommand(f.owner, builtinCommand(t, item.instance, item.input))
		if err != nil {
			t.Fatal(err)
		}
		actual := item.expected.ProtoReflect().New().Interface()
		if err = result.Result.UnmarshalTo(actual); err != nil || !proto.Equal(actual, item.expected) {
			t.Fatalf("Прежний ключ не сохранён: %v", err)
		}
	}
}
