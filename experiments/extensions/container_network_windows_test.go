//go:build windows

package extensions

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func echoEndpoint(t *testing.T, network string) string {
	t.Helper()
	address := "127.0.0.1:0"
	if strings.HasSuffix(network, "6") {
		address = "[::1]:0"
	}
	if strings.HasPrefix(network, "udp") {
		return udpEcho(t, network, address)
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			_, _ = io.Copy(connection, connection)
			connection.Close()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	return listener.Addr().String()
}

func udpEcho(t *testing.T, network, address string) string {
	t.Helper()
	listener, err := net.ListenPacket(network, address)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var buffer [16]byte
		for {
			count, peer, err := listener.ReadFrom(buffer[:])
			if err != nil {
				return
			}
			_, _ = listener.WriteTo(buffer[:count], peer)
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	return listener.LocalAddr().String()
}

func networkResults(t *testing.T, h *ProcessHost, network, address string) map[string]resourceResult {
	t.Helper()
	request, _ := json.Marshal(map[string]string{"Mode": "network", "Network": network, "Address": address})
	input, _ := json.Marshal(workerRequest{Request: request})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := h.run(ctx, input)
	if err != nil {
		t.Fatalf("%s %v", raw, err)
	}
	var results map[string]resourceResult
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatal(string(raw))
	}
	t.Logf("%s %s", network, raw)
	winEmpty(t, h)
	return results
}

func TestWindowsRestrictedNetworkMatrix(t *testing.T) {
	h := restrictedHost(t, "P1_RIGHTS")
	control, err := NewProcessHost(t.TempDir(), os.Getenv("P1_RIGHTS"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	for _, network := range []string{"tcp4", "tcp6", "udp4", "udp6"} {
		t.Run(network, func(t *testing.T) {
			address := echoEndpoint(t, network)
			for name, result := range networkResults(t, control, network, address) {
				if !result.Allowed {
					t.Fatalf("control %s: %+v", name, result)
				}
			}
			results := networkResults(t, h, network, address)
			assertNetworkBoundary(t, results)
		})
	}
}

func assertNetworkBoundary(t *testing.T, results map[string]resourceResult) {
	t.Helper()
	// Раздельно учитываем недоступный Winsock и отказ сетевого доступа.
	for _, name := range []string{"socket", "exchange", "bind"} {
		result := results[name]
		if result.Allowed {
			t.Fatalf("LPAC %s succeeded", name)
		}
		if results["startup"].Allowed {
			if result.Code != 10013 {
				t.Fatalf("Ожидался WSAEACCES: %+v", results)
			}
		} else if results["startup"].Code != 10107 || result.Code != 10093 {
			t.Fatalf("Неожиданный отказ Winsock: %+v", results)
		}
	}
}
