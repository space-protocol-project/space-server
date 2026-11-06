package postgres

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

type deliveryFailureWriter struct {
	http.ResponseWriter
	ctx    context.Context
	resume <-chan struct{}
	fault  string
}

func (w *deliveryFailureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *deliveryFailureWriter) Write(body []byte) (int, error) {
	// Отправляем только префикс: даже HTTP/1.1-клиент не может принять
	// полный успешный документ до управляемого сетевого сбоя.
	n, err := w.ResponseWriter.Write(body[:1])
	if err != nil {
		return n, err
	}
	if err = http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
		return n, err
	}
	select {
	case <-w.ctx.Done():
		return n, w.ctx.Err()
	case <-w.resume:
	}
	if w.fault == "short" {
		return n, nil
	}
	return n, io.ErrClosedPipe
}

func TestExtensionP1DeliveryHTTPNetworkFailure(t *testing.T) {
	for _, item := range []struct{ mode, fault string }{
		{"tls2", "cancel"}, {"tls1", "disconnect"}, {"tls2", "disconnect"},
		{"tls2", "short"}, {"tls2", "write_error"},
	} {
		t.Run(item.mode+"_"+item.fault, func(t *testing.T) { deliveryNetworkFailure(t, item.mode, item.fault) })
	}
}

func deliveryNetworkFailure(t *testing.T, mode, fault string) {
	f := newChannelFixture(t)
	c := f.create(t, "wire_failure")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	member, _ := f.enroll(t, true)
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, new(extensionP1Probe), mutationBindings(c.Id, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	_, handler := s.NewDeliveryHandler()
	resume, returned := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	server := newDeliveryTestServer(t, mode, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if first.CompareAndSwap(false, true) {
			defer close(returned)
			handler.ServeHTTP(&deliveryFailureWriter{w, r.Context(), resume, fault}, r)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer close(resume)
	read := make(chan struct{})
	observed := *server.Client()
	observed.Transport = deliveryReadTransport{observed.Transport, read}
	client := rpc.NewExtensionP1ServiceClient(&observed, server.URL)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result, err := client.QueryExtension(ctx, deliveryQueryRequest(member))
		if result != nil {
			t.Error("Принят неполный ответ")
		}
		done <- err
	}()
	deliverySignal(t, read)
	changed := make(chan error, 1)
	go func() {
		updated, err := f.channels.UpdateChannelAccess(f.owner, &pb.UpdateChannelAccessRequest{ChannelId: c.Id, ExpectedRevision: c.Revision, Rules: []*pb.ChannelAccessRule{roleRule("member", permissions(false, false, false, false))}})
		if err == nil {
			c.Revision = updated.Revision
		}
		changed <- err
	}()
	waitExtensionSQLLock(t, f, "FROM channels WHERE id")
	switch fault {
	case "cancel":
		cancel()
	case "disconnect":
		server.CloseClientConnections()
	default:
		resume <- struct{}{}
	}
	if err := deliveryAwait(t, done); err == nil {
		t.Fatal("Сетевой сбой принят как успех")
	}
	deliverySignal(t, returned)
	if err := deliveryAwait(t, changed); err != nil {
		t.Fatal("Отзыв остался заблокирован", err)
	}
	// Обычный клиент после сбоя использует тот же сервер: отказ по новым
	// правам и успешный вызов после новой выдачи проверяют живой тракт.
	fresh := rpc.NewExtensionP1ServiceClient(server.Client(), server.URL)
	_, err = fresh.QueryExtension(f.ctx, deliveryQueryRequest(member))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal("Отзыв не действует", err)
	}
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	result, err := fresh.QueryExtension(f.ctx, deliveryQueryRequest(member))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Msg.DocumentJson) != `{"ui_api":"space.ui.p1"}` {
		t.Fatal("Неверный документ после сбоя")
	}
}

func deliveryQueryRequest(ctx context.Context) *connect.Request[pb.QueryExtensionRequest] {
	r := connect.NewRequest(extensionP1Query())
	r.Header().Set("Authorization", outgoingAuthorization(ctx))
	return r
}
