package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/space-protocol-project/space-server/internal/home"
	"github.com/space-protocol-project/space-server/internal/postgres"
)

type testNetworkSigner struct{ key ed25519.PrivateKey }

func (s testNetworkSigner) SignOriginBinding(context.Context, string, string, string, string) (string, error) {
	return "unused", nil
}
func (s testNetworkSigner) SignEndpoints(_ context.Context, _ string, payload []byte) (string, error) {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.key, payload)), nil
}

func TestLiveNetworkSignsNewRouteAndRejectsRollback(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	_ = os.Mkdir(data, 0700)
	config := home.NetworkConfig{Endpoints: []home.Endpoint{{Host: "8.8.4.4", Port: 8443, Type: "https"}}, Revision: 1, ListenHost: "::", ListenPort: 8443, AutoPublicIP: true}
	save := func() {
		t.Helper()
		content, _ := json.Marshal(config)
		if err := os.WriteFile(filepath.Join(root, "config.json"), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	public, key, _ := ed25519.GenerateKey(rand.Reader)
	handler, err := bindHomeOrigin(context.Background(), http.NotFoundHandler(), testNetworkSigner{key}, postgres.Identity{ServerID: "srv_test", PublicKey: public}, "https://8.8.4.4:8443", "https://8.8.4.4:8443", tls.Certificate{Certificate: [][]byte{{1, 2, 3}}}, data, true)
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(host string) (int, map[string]any) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "https://"+host+"/.well-known/space-protocol", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var value map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &value)
		return response.Code, value
	}
	if code, _ := fetch("evil.example"); code != 403 {
		t.Fatal(code)
	}
	if _, err := home.UpdateAutomaticEndpoint(data, netip.MustParseAddrPort("8.8.8.8:8443"), true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1050 * time.Millisecond)
	code, value := fetch("8.8.8.8:8443")
	if code != 200 || value["endpoint_revision"] != float64(2) || value["identity_origin"] != "https://8.8.4.4:8443" {
		t.Fatal(code, value)
	}
	var endpoints []home.Endpoint
	encoded, _ := json.Marshal(value["endpoints"])
	_ = json.Unmarshal(encoded, &endpoints)
	payload, _ := home.EndpointPayload("srv_test", "https://8.8.4.4:8443", 2, value["tls_fingerprint"].(string), endpoints)
	signature, _ := base64.RawURLEncoding.DecodeString(value["endpoint_signature"].(string))
	if !ed25519.Verify(public, payload, signature) {
		t.Fatal("подпись не проверена")
	}
	if code, _ := fetch("8.8.4.4:8443"); code != 200 {
		t.Fatal("старый маршрут потерян", code)
	}
	save()
	time.Sleep(1050 * time.Millisecond)
	if code, _ := fetch("8.8.4.4:8443"); code != 503 {
		t.Fatal("откат принят", code)
	}
}
