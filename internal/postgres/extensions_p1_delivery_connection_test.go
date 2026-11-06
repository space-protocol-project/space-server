package postgres

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

func deliverySlowFixture(t *testing.T) (*channelFixture, *pb.Channel, context.Context, http.Handler) {
	t.Helper()
	f := newChannelFixture(t)
	c := f.create(t, "wire_connection")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	member, _ := f.enroll(t, true)
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, new(extensionP1Probe), mutationBindings(c.Id, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	_, handler := s.NewDeliveryHandler()
	return f, c, member, handler
}

func deliveryStartRevoke(f *channelFixture, c *pb.Channel) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := f.channels.UpdateChannelAccess(f.owner, &pb.UpdateChannelAccessRequest{ChannelId: c.Id, ExpectedRevision: c.Revision, Rules: []*pb.ChannelAccessRule{roleRule("member", permissions(false, false, false, false))}})
		done <- err
	}()
	return done
}

func TestExtensionP1DeliveryHTTPConnectionWindow(t *testing.T) {
	f, channel, member, handler := deliverySlowFixture(t)
	var active atomic.Int32
	handler = deliveryCountedHandler(handler, &active)
	server := newDeliveryTestServer(t, "tls2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fill-window" {
			// Служебный ответ стенда расходует ровно исходное общее окно.
			// Сам обработчик расширений и его лимиты не меняются.
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write(bytes.Repeat([]byte{'x'}, 65535))
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	client := newDeliveryH2Client(t, server, 1<<20)
	defer client.conn.Close()
	authorization := outgoingAuthorization(member)
	client.request(t, 1, authorization, "/fill-window")
	fill := client.response(1)
	client.until(t, func() bool { return fill.ended || fill.reset })
	if fill.status != "200" || fill.reset || len(fill.body) != 65535 {
		t.Fatal("Общее окно не исчерпано")
	}
	for _, id := range []uint32{3, 5} {
		client.query(t, id, authorization)
		r := client.response(id)
		client.until(t, func() bool { return r.status != "" })
		if r.status != "200" || r.ended || r.reset || len(r.body) != 0 {
			t.Fatal("Поток не ждёт общего окна", id, r)
		}
	}
	independent := newDeliveryH2Client(t, server, 65535)
	defer independent.conn.Close()
	independent.query(t, 1, authorization)
	r := independent.response(1)
	independent.until(t, func() bool { return r.ended || r.reset })
	verifyDeliveryH2Document(t, r)
	if active.Load() != 2 {
		t.Fatal("Параллельный запрос не завершился при двух активных отправках", active.Load())
	}
	changed := deliveryStartRevoke(f, channel)
	waitExtensionSQLLock(t, f, "FROM channels WHERE id")
	client.until(t, func() bool { return client.response(3).reset && client.response(5).reset })
	for _, id := range []uint32{3, 5} {
		r := client.response(id)
		if r.ended || len(r.body) != 0 {
			t.Fatal("Принят ответ без общего окна", id, r)
		}
	}
	if err := deliveryAwait(t, changed); err != nil {
		t.Fatal("Отзыв остался заблокирован", err)
	}
	client.window(t, 0, 65535)
	verifyDeliveryRevoked(t, client, 7, authorization)
}

func TestExtensionP1DeliveryHTTPConcurrentRecipients(t *testing.T) {
	f, channel, member, handler := deliverySlowFixture(t)
	var active atomic.Int32
	handler = deliveryCountedHandler(handler, &active)
	server := newDeliveryTestServer(t, "tls2", handler)
	defer server.Close()
	authorization := outgoingAuthorization(member)
	var clients []*deliveryH2Client
	for range 2 {
		client := newDeliveryH2Client(t, server, 0)
		defer client.conn.Close()
		client.query(t, 1, authorization)
		r := client.response(1)
		client.until(t, func() bool { return r.status != "" })
		if r.status != "200" || r.ended || r.reset {
			t.Fatal("Медленный получатель не подготовлен")
		}
		clients = append(clients, client)
	}
	independent := newDeliveryH2Client(t, server, 65535)
	defer independent.conn.Close()
	independent.query(t, 1, authorization)
	r := independent.response(1)
	independent.until(t, func() bool { return r.ended || r.reset })
	verifyDeliveryH2Document(t, r)
	if active.Load() != 2 {
		t.Fatal("Параллельный запрос не завершился при двух активных отправках", active.Load())
	}
	changed := deliveryStartRevoke(f, channel)
	waitExtensionSQLLock(t, f, "FROM channels WHERE id")
	for _, client := range clients {
		r := client.response(1)
		client.until(t, func() bool { return r.reset || r.ended })
		if !r.reset || r.ended || len(r.body) != 0 {
			t.Fatal("Медленный поток не оборван")
		}
	}
	if err := deliveryAwait(t, changed); err != nil {
		t.Fatal("Отзыв остался заблокирован", err)
	}
	for _, client := range clients {
		client.query(t, 3, authorization)
		client.window(t, 3, 65535)
		verifyDeliveryRevokedResponse(t, client, 3)
	}
}

func verifyDeliveryRevoked(t *testing.T, client *deliveryH2Client, id uint32, authorization string) {
	t.Helper()
	client.query(t, id, authorization)
	verifyDeliveryRevokedResponse(t, client, id)
}

func verifyDeliveryRevokedResponse(t *testing.T, client *deliveryH2Client, id uint32) {
	t.Helper()
	r := client.response(id)
	client.until(t, func() bool { return r.ended || r.reset })
	if r.status != "404" || !r.ended || r.reset {
		t.Fatal("Соединение или отзыв после тайм-аута", r)
	}
}

func deliveryCountedHandler(next http.Handler, active *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Add(-1)
		next.ServeHTTP(w, r)
	})
}
