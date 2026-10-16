package home

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWitnessRequiresDirectCompletedTLSDiscovery(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://space.example/.well-known/space-protocol", nil)
	request.TLS = &tls.ConnectionState{HandshakeComplete: true}
	if !witnessRequest(request) {
		t.Fatal("Отклонён HTTPS discovery")
	}
	for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Real-IP", "Via"} {
		request.Header.Set(header, "1.1.1.1")
		if witnessRequest(request) {
			t.Fatal("Принят заголовок прокси", header)
		}
		request.Header.Del(header)
	}
	request.TLS.HandshakeComplete = false
	if witnessRequest(request) {
		t.Fatal("Незавершённый TLS")
	}
	request.TLS = nil
	if witnessRequest(request) {
		t.Fatal("Принят обычный HTTP")
	}
}
