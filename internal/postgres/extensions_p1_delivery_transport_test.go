package postgres

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

var deliveryTransportModes = []string{"http1", "tls1", "tls2"}

func newDeliveryTestServer(t *testing.T, mode string, handler http.Handler) *httptest.Server {
	t.Helper()
	major := 1
	if mode == "tls2" {
		major = 2
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != major || (r.TLS != nil) != (mode != "http1") {
			t.Errorf("Неверный транспорт: mode=%s proto=%s TLS=%v", mode, r.Proto, r.TLS != nil)
			http.Error(w, "transport mismatch", http.StatusBadRequest)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	server.EnableHTTP2 = mode == "tls2"
	if mode == "http1" {
		server.Start()
	} else {
		server.StartTLS()
	}
	return server
}

type deliveryReadTransport struct {
	http.RoundTripper
	read chan struct{}
}

func (t deliveryReadTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.RoundTripper.RoundTrip(r)
	if err == nil {
		response.Body = &deliveryReadBody{ReadCloser: response.Body, read: t.read}
	}
	return response, err
}

type deliveryReadBody struct {
	io.ReadCloser
	read chan struct{}
	once sync.Once
}

func (b *deliveryReadBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		// HTTP/2 может доставить тело до END_STREAM; не ждём окончания RPC,
		// пока сервер намеренно удерживает транзакцию и завершение потока.
		b.once.Do(func() { close(b.read) })
	}
	return n, err
}
