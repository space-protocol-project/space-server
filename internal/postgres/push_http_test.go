package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/push"
	"google.golang.org/grpc/metadata"
)

func pushRequest(handler http.Handler, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}
func fixtureToken(ctx context.Context) string {
	md, _ := metadata.FromOutgoingContext(ctx)
	return strings.TrimPrefix(md.Get("authorization")[0], "Bearer ")
}
func registerFixturePush(t *testing.T, f *channelFixture, ctx context.Context) (string, []byte) {
	t.Helper()
	key := bytes.Repeat([]byte{9}, 32)
	body, _ := json.Marshal(map[string]string{"binding": strings.Repeat("a", 32), "key": base64.RawURLEncoding.EncodeToString(key)})
	response := pushRequest(f.store.WithPush(http.NotFoundHandler()), "/api/push/register", fixtureToken(ctx), string(body))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var result map[string]string
	json.Unmarshal(response.Body.Bytes(), &result)
	return result["token"], key
}
func pushContext(t *testing.T, s *Store, ctx context.Context, token string) context.Context {
	t.Helper()
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
	result, err := authn.AuthorizeCall(ctx, pushVerifier{s}, "/private/push")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPrivatePushDurableQueueCapabilityAndAck(t *testing.T) {
	f := newChannelFixture(t)
	member, _ := f.enroll(t, false)
	token, key := registerFixturePush(t, f, member)
	activeHash := sha256.Sum256([]byte(token))
	f.store.pushActive.Store(activeHash, true)
	if pushRequest(f.store.WithPush(http.NotFoundHandler()), "/api/push/poll", token, `{"ack":[]}`).Code != 429 {
		t.Fatal("допущена вторая подписка устройства")
	}
	f.store.pushActive.Delete(activeHash)
	if _, err := f.store.Authenticate(f.ctx, token); err == nil {
		t.Fatal("push capability принята обычным RPC")
	}
	for i, text := range []string{"first secret", "second secret"} {
		if _, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: text, IdempotencyKey: strings.Repeat("x", i+1)}); err != nil {
			t.Fatal(err)
		}
	}
	// Очередь объединяет сигналы, переживает повторное открытие Store.
	reopened, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	handler := reopened.WithPush(http.NotFoundHandler())
	response := pushRequest(handler, "/api/push/poll", token, `{"ack":[]}`)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret") {
		t.Fatal("текст попал в push")
	}
	var frames []string
	if json.Unmarshal(response.Body.Bytes(), &frames) != nil || len(frames) != 1 {
		t.Fatal(response.Body.String())
	}
	target, err := push.Open(key, frames[0], time.Now())
	if err != nil || target.Sequence != 2 || target.Channel != "general" {
		t.Fatal(target, err)
	}
	hash := sha256.Sum256([]byte(token))
	ctx := pushContext(t, reopened, f.ctx, token)
	repeated, err := reopened.pushBatch(ctx, hash[:], nil)
	if err != nil || len(repeated) != 1 {
		t.Fatal(repeated, err)
	}
	acked, err := reopened.pushBatch(ctx, hash[:], []string{target.Event})
	if err != nil || len(acked) != 0 {
		t.Fatal(acked, err)
	}
	// Повторная регистрация не теряет недоставленный сигнал.
	if _, err = f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "third", IdempotencyKey: "third"}); err != nil {
		t.Fatal(err)
	}
	newToken, _ := registerFixturePush(t, f, member)
	if pushRequest(handler, "/api/push/poll", token, `{"ack":[]}`).Code != 401 {
		t.Fatal("старый ключ не отозван")
	}
	next := pushRequest(handler, "/api/push/poll", newToken, `{"ack":[]}`)
	if next.Code != 200 {
		t.Fatal(next.Code)
	}
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", authn.Actor(pushContext(t, f.store, f.ctx, newToken))); err != nil {
		t.Fatal(err)
	}
	if pushRequest(handler, "/api/push/poll", newToken, `{"ack":[]}`).Code != 401 {
		t.Fatal("отозванное устройство получило сигнал")
	}
}

func TestPrivatePushRechecksAclAndNeverAcceptsAnonymous(t *testing.T) {
	f := newChannelFixture(t)
	member, _ := f.enroll(t, false)
	channel := f.create(t, "push_secret")
	f.acl(t, channel, roleRule("member", permissions(true, true, true, false)))
	token, _ := registerFixturePush(t, f, member)
	if _, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: channel.Id, Text: "secret", IdempotencyKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	f.acl(t, channel, roleRule("member", permissions(false, false, false, false)))
	hash := sha256.Sum256([]byte(token))
	ctx := pushContext(t, f.store, f.ctx, token)
	batch, err := f.store.pushBatch(ctx, hash[:], nil)
	if err != nil || len(batch) != 0 {
		t.Fatal(batch, err)
	}
	handler := f.store.WithPush(http.NotFoundHandler())
	if pushRequest(handler, "/api/push/register", "", `{}`).Code != 401 {
		t.Fatal("анонимная регистрация")
	}
	if pushRequest(handler, "/api/push/register", token, `{}`).Code != 401 {
		t.Fatal("push capability расширила права")
	}
}

func TestPrivatePushMlsMembershipAndRemoval(t *testing.T) {
	f := newChannelFixture(t)
	alice, _ := enrollMls(t, f, true)
	bob, b := enrollMls(t, f, false)
	client := pb.NewMlsServiceClient(f.conn)
	group := bytes.Repeat([]byte{42}, 32)
	_, err := f.channels.CreateChannel(alice, &pb.CreateChannelRequest{ChannelId: "private_push", Title: "Закрытый", ViewType: "chat", MlsGroupId: group})
	if err != nil {
		t.Fatal(err)
	}
	token, key := registerFixturePush(t, f, bob)
	_, err = client.PublishMls(alice, &pb.PublishMlsRequest{ChannelId: "private_push", RequestId: bytes.Repeat([]byte{1}, 16), Kind: "commit", Ciphertext: []byte("commit"), AddGrantId: b.GrantId, Welcome: []byte("welcome")})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(token))
	ctx := pushContext(t, f.store, f.ctx, token)
	batch, err := f.store.pushBatch(ctx, hash[:], nil)
	if err != nil || len(batch) != 0 {
		t.Fatal("commit создал карточку", batch, err)
	}
	_, err = client.PublishMls(alice, &pb.PublishMlsRequest{ChannelId: "private_push", RequestId: bytes.Repeat([]byte{2}, 16), ExpectedEpoch: 1, Kind: "application", Ciphertext: []byte("opaque")})
	if err != nil {
		t.Fatal(err)
	}
	batch, err = f.store.pushBatch(ctx, hash[:], nil)
	if err != nil || len(batch) != 1 {
		t.Fatal(batch, err)
	}
	target, err := push.Open(key, batch[0], time.Now())
	if err != nil || target.Group != base64.StdEncoding.EncodeToString(group) || target.Sequence != 2 {
		t.Fatal(target, err)
	}
	_, err = client.PublishMls(alice, &pb.PublishMlsRequest{ChannelId: "private_push", RequestId: bytes.Repeat([]byte{3}, 16), ExpectedEpoch: 1, Kind: "commit", Ciphertext: []byte("remove"), RemoveGrantId: b.GrantId})
	if err != nil {
		t.Fatal(err)
	}
	batch, err = f.store.pushBatch(ctx, hash[:], nil)
	if err != nil || len(batch) != 0 {
		t.Fatal("удалённый leaf получил сигнал", batch, err)
	}
}
