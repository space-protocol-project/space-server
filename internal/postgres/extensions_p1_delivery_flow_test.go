package postgres

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestExtensionP1DeliveryHTTPFlowControl(t *testing.T) {
	for _, mode := range []string{"zero_window", "one_byte", "resume"} {
		t.Run(mode, func(t *testing.T) { deliveryFlowControl(t, mode) })
	}
}

func deliveryFlowControl(t *testing.T, mode string) {
	f := newChannelFixture(t)
	c := f.create(t, "wire_flow")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	member, _ := f.enroll(t, true)
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, new(extensionP1Probe), mutationBindings(c.Id, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	_, handler := s.NewDeliveryHandler()
	returned := make(chan struct{})
	var first atomic.Bool
	server := newDeliveryTestServer(t, "tls2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if first.CompareAndSwap(false, true) {
			defer close(returned)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	window := uint32(0)
	if mode == "one_byte" {
		window = 1
	}
	client := newDeliveryH2Client(t, server, window)
	defer client.conn.Close()
	started := time.Now()
	authorization := outgoingAuthorization(member)
	client.query(t, 1, authorization)
	slow := client.response(1)
	client.until(t, func() bool { return slow.status != "" && len(slow.body) == int(window) })
	if slow.status != "200" || slow.ended || slow.reset {
		t.Fatal("Нет удерживаемого ответа", slow)
	}
	// На том же TLS-соединении другой поток имеет собственное окно.
	client.query(t, 3, authorization)
	client.window(t, 3, 65535)
	parallel := client.response(3)
	client.until(t, func() bool { return parallel.ended || parallel.reset })
	verifyDeliveryH2Document(t, parallel)
	select {
	case <-returned:
		t.Fatal("Первый поток уже завершён")
	default:
	}
	changed := make(chan error, 1)
	go func() {
		_, err := f.channels.UpdateChannelAccess(f.owner, &pb.UpdateChannelAccessRequest{ChannelId: c.Id, ExpectedRevision: c.Revision, Rules: []*pb.ChannelAccessRule{roleRule("member", permissions(false, false, false, false))}})
		changed <- err
	}()
	waitExtensionSQLLock(t, f, "FROM channels WHERE id")
	if mode == "resume" {
		client.window(t, 1, 65535)
	}
	client.until(t, func() bool { return slow.ended || slow.reset })
	if mode == "resume" {
		verifyDeliveryH2Document(t, slow)
	} else if !slow.reset || slow.ended || len(slow.body) != int(window) {
		t.Fatal("Зависший поток не оборван", slow)
	}
	deliverySignal(t, returned)
	if err := deliveryAwait(t, changed); err != nil {
		t.Fatal("Отзыв не освободился", err)
	}
	t.Logf("flow_mode=%s release_ms=%d", mode, time.Since(started).Milliseconds())
	// Тайм-аут одного потока не должен закрыть соединение: следующий
	// запрос проходит по нему же и получает уже обновлённый результат ACL.
	client.query(t, 5, authorization)
	client.window(t, 5, 65535)
	after := client.response(5)
	client.until(t, func() bool { return after.ended || after.reset })
	if after.status != "404" || !after.ended || after.reset {
		t.Fatal("Соединение/ACL после сбоя", after)
	}
}

func verifyDeliveryH2Document(t *testing.T, response *deliveryH2Response) {
	t.Helper()
	if response.status != "200" || !response.ended || response.reset {
		t.Fatal("Неполный параллельный ответ", response)
	}
	var result pb.QueryExtensionResponse
	if err := protojson.Unmarshal(response.body, &result); err != nil {
		t.Fatal(err)
	}
	if string(result.DocumentJson) != `{"ui_api":"space.ui.p1"}` {
		t.Fatal("Неверный документ")
	}
}
