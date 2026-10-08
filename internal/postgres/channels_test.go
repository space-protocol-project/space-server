package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"github.com/space-protocol-project/space-protocol/server/internal/chat"
	"github.com/space-protocol-project/space-protocol/server/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type channelFixture struct {
	ctx      context.Context
	url      string
	store    *Store
	identity Identity
	conn     *grpc.ClientConn
	auth     pb.AuthServiceClient
	channels pb.ChannelServiceClient
	contents pb.ContentServiceClient
	sync     pb.SyncServiceClient
	admin    pb.AdminServiceClient
	owner    context.Context
	ownerID  string
}

func newChannelFixture(t *testing.T) *channelFixture {
	t.Helper()
	ctx, url := isolatedDatabase(t)
	store, identity, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(authn.Interceptor(store)), grpc.StreamInterceptor(authn.StreamInterceptor(store)))
	service := chat.NewPersistent(identity.ServerID, store)
	pb.RegisterChannelServiceServer(server, service)
	pb.RegisterContentServiceServer(server, service)
	pb.RegisterSyncServiceServer(server, store)
	pb.RegisterAdminServiceServer(server, store)
	pb.RegisterAuthServiceServer(server, NewAuth(store, identity.ServerID, "http://127.0.0.1:8080"))
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	f := &channelFixture{ctx: ctx, url: url, store: store, identity: identity, conn: conn, auth: pb.NewAuthServiceClient(conn), channels: pb.NewChannelServiceClient(conn), contents: pb.NewContentServiceClient(conn), sync: pb.NewSyncServiceClient(conn), admin: pb.NewAdminServiceClient(conn)}
	f.owner, f.ownerID = f.enroll(t, true)
	code, err := store.CreateSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.admin.ClaimOwner(f.owner, &pb.ClaimOwnerRequest{SetupCode: code}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *channelFixture) enroll(t *testing.T, administrative bool) (context.Context, string) {
	t.Helper()
	root, private, _ := ed25519.GenerateKey(rand.Reader)
	device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	grant := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device, Administrative: administrative}, private)
	session := complete(t, f.ctx, f.auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: grant.GrantId}, devicePrivate)
	return metadata.AppendToOutgoingContext(f.ctx, "authorization", "Bearer "+session.AccessToken), grant.PrincipalId
}

func permissions(visible, read, write, manage bool) *pb.ChannelPermissions {
	return &pb.ChannelPermissions{Visible: visible, Read: read, Write: write, Manage: manage}
}
func roleRule(role string, p *pb.ChannelPermissions) *pb.ChannelAccessRule {
	return &pb.ChannelAccessRule{Role: role, Permissions: p}
}
func principalRule(id string, p *pb.ChannelPermissions) *pb.ChannelAccessRule {
	return &pb.ChannelAccessRule{PrincipalId: id, Permissions: p}
}
func requireCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if status.Code(err) != code {
		t.Fatalf("Ожидался %s, получен %v", code, err)
	}
}

func (f *channelFixture) create(t *testing.T, id string) *pb.Channel {
	t.Helper()
	c, err := f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: id, Title: "Канал " + id, ViewType: "chat", Position: 10})
	if err != nil {
		t.Fatal(err)
	}
	return c.Channel
}
func (f *channelFixture) acl(t *testing.T, c *pb.Channel, rules ...*pb.ChannelAccessRule) {
	t.Helper()
	result, err := f.channels.UpdateChannelAccess(f.owner, &pb.UpdateChannelAccessRequest{ChannelId: c.Id, ExpectedRevision: c.Revision, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	c.Revision = result.Revision
}

func TestChannelsIsolationPermissionsAndPersistence(t *testing.T) {
	f := newChannelFixture(t)
	member, memberID := f.enroll(t, true)
	other, otherID := f.enroll(t, false)
	secret := f.create(t, "secret")
	another := f.create(t, "another")
	if _, err := f.channels.CreateChannel(member, &pb.CreateChannelRequest{ChannelId: "forbidden", Title: "Чат", ViewType: "chat"}); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	_, err := f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: secret.Id, Title: "Повтор", ViewType: "chat"})
	requireCode(t, err, codes.AlreadyExists)
	_, err = f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: "forum", Title: "Форум", ViewType: "forum"})
	requireCode(t, err, codes.InvalidArgument)
	f.acl(t, secret, principalRule(memberID, permissions(true, true, true, true)))
	first, err := f.contents.CreateContent(member, &pb.CreateContentRequest{ChannelId: secret.Id, Text: "Секрет", IdempotencyKey: "same"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.contents.CreateContent(member, &pb.CreateContentRequest{ChannelId: another.Id, Text: "Другой канал", IdempotencyKey: "same"}); err != nil {
		t.Fatal(err)
	}
	listed, err := f.contents.ListContent(member, &pb.ListContentRequest{ChannelId: secret.Id})
	if err != nil || len(listed.GetContents()) != 1 || listed.Contents[0].Text != "Секрет" {
		t.Fatal(listed, err)
	}
	// Проверка доступа до разбора курсора: секретные данные не выдаются по прямому URL.
	for _, cursor := range []string{"", first.Content.Id, "unknown"} {
		_, err = f.contents.ListContent(other, &pb.ListContentRequest{ChannelId: secret.Id, After: cursor})
		requireCode(t, err, codes.NotFound)
		_, err = f.sync.ListEvents(other, &pb.ListEventsRequest{ChannelId: secret.Id, After: cursor})
		requireCode(t, err, codes.NotFound)
	}
	_, err = f.contents.CreateContent(other, &pb.CreateContentRequest{ChannelId: secret.Id, Text: "Нет", IdempotencyKey: "hidden"})
	requireCode(t, err, codes.NotFound)
	hidden, err := f.channels.GetManifest(other, &pb.GetManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range hidden.Channels {
		if c.Id == secret.Id {
			t.Fatal("Утечка скрытого канала")
		}
	}
	anonymous, err := f.channels.GetManifest(f.ctx, &pb.GetManifestRequest{})
	if err != nil || len(anonymous.GetChannels()) != 1 || anonymous.Channels[0].Id != "general" || anonymous.Channels[0].Permissions != nil {
		t.Fatal(anonymous, err)
	}
	invalid := metadata.AppendToOutgoingContext(f.ctx, "authorization", "Bearer invalid")
	_, err = f.channels.GetManifest(invalid, &pb.GetManifestRequest{})
	requireCode(t, err, codes.Unauthenticated)
	// Управляющий каналом меняет метаданные, но не ACL и не публичность.
	renamed, err := f.channels.UpdateChannel(member, &pb.UpdateChannelRequest{ChannelId: secret.Id, Title: "Рабочая группа", Position: 2, ExpectedRevision: secret.Revision})
	if err != nil {
		t.Fatal(err)
	}
	secret = renamed.Channel
	_, err = f.channels.GetChannelAccess(member, &pb.GetChannelAccessRequest{ChannelId: secret.Id})
	requireCode(t, err, codes.PermissionDenied)
	_, err = f.channels.UpdateChannelAccess(member, &pb.UpdateChannelAccessRequest{ChannelId: secret.Id, ExpectedRevision: secret.Revision})
	requireCode(t, err, codes.PermissionDenied)
	_, err = f.channels.UpdateChannel(member, &pb.UpdateChannelRequest{ChannelId: secret.Id, Title: secret.Title, Position: 2, PublicPreview: true, ExpectedRevision: secret.Revision})
	requireCode(t, err, codes.PermissionDenied)
	_, err = f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: secret.Id, Title: "Конфликт", ExpectedRevision: 1})
	requireCode(t, err, codes.Aborted)
	// Точный override заменяет роль, включая явный запрет.
	f.acl(t, secret, roleRule("member", permissions(true, true, true, false)), principalRule(otherID, permissions(false, false, false, false)))
	_, err = f.contents.ListContent(other, &pb.ListContentRequest{ChannelId: secret.Id})
	requireCode(t, err, codes.NotFound)
	f.acl(t, secret, roleRule("member", permissions(true, false, false, false)))
	visible, err := f.channels.ListChannels(other, &pb.ListChannelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range visible.Channels {
		if c.Id == secret.Id {
			found = true
			if c.Permissions.Read {
				t.Fatal("read разрешён")
			}
		}
	}
	if !found {
		t.Fatal("Канал должен быть видимым")
	}
	_, err = f.contents.ListContent(other, &pb.ListContentRequest{ChannelId: secret.Id})
	requireCode(t, err, codes.PermissionDenied)
	// Роль пространства и scope устройства — верхняя граница разрешений.
	m, err := f.admin.ListMembers(f.owner, &pb.ListMembersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var revision int64
	for _, entry := range m.Members {
		if entry.PrincipalId == otherID {
			revision = entry.Revision
		}
	}
	changed, err := f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: otherID, Role: "reader", ExpectedRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	f.acl(t, secret, principalRule(otherID, permissions(true, true, true, true)))
	_, err = f.contents.CreateContent(other, &pb.CreateContentRequest{ChannelId: secret.Id, Text: "reader", IdempotencyKey: "reader"})
	requireCode(t, err, codes.PermissionDenied)
	_, err = f.channels.UpdateChannel(other, &pb.UpdateChannelRequest{ChannelId: secret.Id, Title: "Нет scope", ExpectedRevision: secret.Revision})
	requireCode(t, err, codes.PermissionDenied)
	// Даже явное разрешение не обходит блокировку пространства.
	_, err = f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: otherID, Role: "reader", Blocked: true, ExpectedRevision: changed.Member.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.contents.ListContent(other, &pb.ListContentRequest{ChannelId: secret.Id})
	requireCode(t, err, codes.PermissionDenied)
	archived, err := f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: secret.Id, Title: secret.Title, Position: 2, Archived: true, ExpectedRevision: secret.Revision})
	if err != nil {
		t.Fatal(err)
	}
	secret = archived.Channel
	_, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: secret.Id, Text: "Архив", IdempotencyKey: "archived"})
	requireCode(t, err, codes.FailedPrecondition)
	if _, err = f.contents.ListContent(f.owner, &pb.ListContentRequest{ChannelId: secret.Id}); err != nil {
		t.Fatal(err)
	}
	active, err := f.channels.ListChannels(f.owner, &pb.ListChannelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range active.Channels {
		if c.Id == secret.Id {
			t.Fatal("Архив в активном списке")
		}
	}
	// Новый Store повторно открывает ту же базу без изменения identity, ACL или истории.
	reopened, id, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if id.ServerID != f.identity.ServerID {
		t.Fatal("Изменилась identity")
	}
	c, err := loadChannel(f.ctx, reopened.pool, secret.Id, "")
	if err != nil || c.Title != secret.Title || !c.Archived || c.Revision != secret.Revision {
		t.Fatal(c, err)
	}
	history, err := reopened.List(f.ctx, &pb.ListContentRequest{ChannelId: secret.Id})
	if err != nil || len(history.GetContents()) != 1 || history.Contents[0].Text != "Секрет" {
		t.Fatal(history, err)
	}
	var count int
	if err = reopened.pool.QueryRow(f.ctx, "SELECT count(*) FROM channel_access WHERE channel_id=$1 AND principal_id=$2", secret.Id, otherID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestChannelACLRevokesExistingGRPCAndHTTPStreams(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, false)
	c := f.create(t, "live")
	handler, err := transport.Handler(f.ctx, f.conn, f.identity.ServerID, f.identity.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(handler)
	defer web.Close()
	ctx, cancel := context.WithTimeout(member, 8*time.Second)
	defer cancel()
	stream, err := f.sync.Subscribe(ctx, &pb.SubscribeRequest{ChannelId: c.Id})
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := stream.Recv(); err != nil || !frame.Heartbeat {
		t.Fatal(frame, err)
	}
	request, _ := http.NewRequestWithContext(ctx, "GET", web.URL+"/api/v1/channels/live/events/subscribe", nil)
	request.Header.Set("Authorization", outgoingAuthorization(member))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	decoder := json.NewDecoder(response.Body)
	var frame struct {
		Result json.RawMessage `json:"result"`
		Error  struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err = decoder.Decode(&frame); err != nil || len(frame.Result) == 0 {
		t.Fatal(frame, err)
	}
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)), principalRule(id, permissions(true, false, false, false)))
	// После завершённого отзыва новая запись не должна попасть в открытые потоки.
	if _, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: c.Id, Text: "После отзыва", IdempotencyKey: "after"}); err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	requireCode(t, err, codes.PermissionDenied)
	frame.Result = nil
	if err = decoder.Decode(&frame); err != nil || frame.Error.Code != int(codes.PermissionDenied) || len(frame.Result) != 0 {
		t.Fatal(frame, err)
	}
	denied, err := f.sync.Subscribe(member, &pb.SubscribeRequest{ChannelId: c.Id})
	if err != nil {
		t.Fatal(err)
	}
	_, err = denied.Recv()
	requireCode(t, err, codes.PermissionDenied)
}

func TestChannelHTTPAndConcurrentRevision(t *testing.T) {
	f := newChannelFixture(t)
	handler, err := transport.Handler(f.ctx, f.conn, f.identity.ServerID, f.identity.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(handler)
	defer web.Close()
	call := func(method, path string, input, output proto.Message, want int) {
		t.Helper()
		var body []byte
		if input != nil {
			body, err = protojson.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, _ := http.NewRequestWithContext(f.ctx, method, web.URL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", outgoingAuthorization(f.owner))
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, data)
		}
		if output != nil {
			if err = protojson.Unmarshal(data, output); err != nil {
				t.Fatal(err)
			}
		}
	}
	created := new(pb.CreateChannelResponse)
	call("POST", "/api/v1/channels", &pb.CreateChannelRequest{ChannelId: "http", Title: "Через HTTP", ViewType: "chat", Position: 1}, created, 200)
	acl := new(pb.UpdateChannelAccessResponse)
	call("PUT", "/api/v1/channels/http/access", &pb.UpdateChannelAccessRequest{ExpectedRevision: created.Channel.Revision, Rules: []*pb.ChannelAccessRule{roleRule("reader", permissions(true, true, false, false))}}, acl, 200)
	loaded := new(pb.GetChannelAccessResponse)
	call("GET", "/api/v1/channels/http/access", nil, loaded, 200)
	if !proto.Equal(&pb.GetChannelAccessResponse{ChannelId: acl.ChannelId, Revision: acl.Revision, Rules: acl.Rules}, loaded) {
		t.Fatal(loaded, acl)
	}
	call("PATCH", "/api/v1/channels/http", &pb.UpdateChannelRequest{Title: "Через HTTP", Position: 1, ExpectedRevision: acl.Revision}, new(pb.UpdateChannelResponse), 200)
	channels, err := f.channels.ListChannels(f.owner, &pb.ListChannelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var c *pb.Channel
	for _, entry := range channels.Channels {
		if entry.Id == "http" {
			c = entry
		}
	}
	if c == nil {
		t.Fatal("Нет канала")
	}
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.channels.UpdateChannelAccess(f.owner, &pb.UpdateChannelAccessRequest{ChannelId: c.Id, ExpectedRevision: c.Revision})
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	successes, conflicts := 0, 0
	for err := range errors {
		switch status.Code(err) {
		case codes.OK:
			successes++
		case codes.Aborted:
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
	// Метаданные публичного preview доступны без токена, содержимое — только с сессией.
	public := f.create(t, "preview")
	_, err = f.channels.UpdateChannel(f.owner, &pb.UpdateChannelRequest{ChannelId: public.Id, Title: public.Title, PublicPreview: true, ExpectedRevision: public.Revision})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(web.URL + "/api/v1/manifest")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(data), "preview") || strings.Contains(string(data), "Через HTTP") {
		t.Fatal(string(data))
	}
	response, err = http.Get(web.URL + "/api/v1/channels/preview/content")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal(response.StatusCode)
	}
}

func TestChannelRulesValidation(t *testing.T) {
	for _, rules := range [][]*pb.ChannelAccessRule{
		{roleRule("owner", permissions(true, true, true, true))},
		{roleRule("member", permissions(false, true, false, false))},
		{roleRule("member", permissions(true, false, true, false))},
		{roleRule("member", permissions(false, false, false, true))},
		{roleRule("member", permissions(true, true, true, false)), roleRule("member", permissions(false, false, false, false))},
		{{Role: "member", PrincipalId: "u_test", Permissions: permissions(true, true, false, false)}},
		{nil},
	} {
		requireCode(t, validateRules(rules), codes.InvalidArgument)
	}
	if err := validateRules(nil); err != nil {
		t.Fatal(err)
	}
}

func TestDisabledChatKeepsChannelMetadataForConfiguration(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "disabled")
	settings, err := f.admin.GetSettings(f.owner, &pb.GetSettingsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.admin.UpdateSettings(f.owner, &pb.UpdateSettingsRequest{Title: settings.Settings.Title, ChatTitle: settings.Settings.ChatTitle, ChatEnabled: false, RegistrationPolicy: "open", ExpectedRevision: settings.Settings.Revision})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := f.channels.ListChannels(f.owner, &pb.ListChannelsRequest{IncludeArchived: true})
	if err != nil || len(listed.GetChannels()) != 2 {
		t.Fatal(listed, err)
	}
	for _, entry := range listed.Channels {
		if entry.Permissions.Read || entry.Permissions.Write || !entry.Permissions.Manage {
			t.Fatal(entry)
		}
	}
	_, err = f.contents.ListContent(f.owner, &pb.ListContentRequest{ChannelId: c.Id})
	requireCode(t, err, codes.NotFound)
	manifest, err := f.channels.GetManifest(f.owner, &pb.GetManifestRequest{})
	if err != nil || len(manifest.GetChannels()) != 0 {
		t.Fatal(manifest, err)
	}
}

func outgoingAuthorization(ctx context.Context) string {
	md, _ := metadata.FromOutgoingContext(ctx)
	return md.Get("authorization")[0]
}

type capturedChannelStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent bool
}

func (s *capturedChannelStream) Context() context.Context         { return s.ctx }
func (s *capturedChannelStream) Send(*pb.SubscribeResponse) error { s.sent = true; return nil }

func TestChannelRevocationRejectsAlreadyLoadedEvent(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, false)
	c := f.create(t, "cached")
	if _, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: c.Id, Text: "Загружено до отзыва", IdempotencyKey: "cached"}); err != nil {
		t.Fatal(err)
	}
	var authorized context.Context
	incoming := metadata.NewIncomingContext(f.ctx, metadata.Pairs("authorization", outgoingAuthorization(member)))
	_, err := authn.Interceptor(f.store)(incoming, nil, &grpc.UnaryServerInfo{FullMethod: "/space.v1.SyncService/ListEvents"}, func(ctx context.Context, _ any) (any, error) { authorized = ctx; return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	batch, err := f.store.ListEvents(authorized, &pb.ListEventsRequest{ChannelId: c.Id})
	if err != nil || len(batch.GetEvents()) != 1 {
		t.Fatal(batch, err)
	}
	f.acl(t, c, principalRule(id, permissions(false, false, false, false)))
	stream := &capturedChannelStream{ctx: authorized}
	err = f.store.sendAuthorizedFrame(c.Id, stream, &pb.SubscribeResponse{Event: batch.Events[0], Cursor: batch.Events[0].Cursor})
	requireCode(t, err, codes.NotFound)
	if stream.sent {
		t.Fatal("Событие из старой пачки выдано после отзыва")
	}
}
