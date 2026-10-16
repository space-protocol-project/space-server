package home

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type namespaceInfo struct {
	Endpoint Endpoint
	Pin, ID  string
}

func netnsCommand(t *testing.T, args ...string) {
	t.Helper()
	if output, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
		t.Fatalf("ip %v: %v %s", args, err, output)
	}
}

func namespaceHelper(t *testing.T, namespace, mode, directory string) *exec.Cmd {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("ip", "netns", "exec", namespace, binary, "-test.run=^TestWitnessNamespaceHelper$", "-test.v", "-test.timeout=40s")
	command.Env = append(os.Environ(), "SPACE_WITNESS_HELPER="+mode, "SPACE_WITNESS_DIRECTORY="+directory)
	return command
}

func runNamespaceHelper(t *testing.T, namespace, mode, directory string) {
	t.Helper()
	if output, err := namespaceHelper(t, namespace, mode, directory).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v %s", mode, err, output)
	}
}

func TestWitnessNetworkNamespaces(t *testing.T) {
	if os.Getenv("SPACE_NETWORK_NETNS") != "1" {
		t.Skip("Нужны отдельные Linux netns и root в CI")
	}
	if os.Geteuid() != 0 {
		t.Fatal("Тест должен запускаться в выделенном root CI")
	}
	suffix := os.Getpid()
	serverNS := fmt.Sprintf("space-witness-s-%d", suffix)
	clientNS := fmt.Sprintf("space-witness-c-%d", suffix)
	for _, namespace := range []string{serverNS, clientNS} {
		netnsCommand(t, "netns", "add", namespace)
		t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", namespace).Run() })
		netnsCommand(t, "-n", namespace, "link", "set", "lo", "up")
	}
	serverLink := fmt.Sprintf("ws%d", suffix)
	clientLink := fmt.Sprintf("wc%d", suffix)
	netnsCommand(t, "link", "add", serverLink, "type", "veth", "peer", "name", clientLink)
	netnsCommand(t, "link", "set", serverLink, "netns", serverNS)
	netnsCommand(t, "link", "set", clientLink, "netns", clientNS)
	// Изолированные /32, без маршрута в интернет. Адреса должны пройти фильтр публичного диапазона.
	for _, side := range []struct{ namespace, link, ip, peer string }{{serverNS, serverLink, "11.10.0.1", "11.20.0.1"}, {clientNS, clientLink, "11.20.0.1", "11.10.0.1"}} {
		netnsCommand(t, "-n", side.namespace, "address", "add", side.ip+"/32", "dev", side.link)
		netnsCommand(t, "-n", side.namespace, "link", "set", side.link, "up")
		netnsCommand(t, "-n", side.namespace, "route", "add", side.peer+"/32", "dev", side.link)
	}
	directory := t.TempDir()
	server := namespaceHelper(t, serverNS, "server", directory)
	server.Stdout = os.Stdout
	server.Stderr = os.Stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Process.Kill(); _ = server.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(directory, "ready.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Не запущен сервер netns")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, attempt := range []struct{ namespace, mode, want string }{{serverNS, "client", "unverified"}, {clientNS, "forwarded", "unverified"}, {clientNS, "client", "observed"}} {
		runNamespaceHelper(t, attempt.namespace, attempt.mode, directory)
		runNamespaceHelper(t, serverNS, "doctor", directory)
		content, err := os.ReadFile(filepath.Join(directory, "report.json"))
		if err != nil {
			t.Fatal(err)
		}
		var report DoctorReport
		if err := json.Unmarshal(content, &report); err != nil {
			t.Fatal(err)
		}
		if report.External != attempt.want {
			t.Fatalf("%s/%s: %+v", attempt.namespace, attempt.mode, report)
		}
	}
}

func TestWitnessNamespaceHelper(t *testing.T) {
	mode := os.Getenv("SPACE_WITNESS_HELPER")
	if mode == "" {
		t.Skip("Дочерний процесс netns")
	}
	directory := os.Getenv("SPACE_WITNESS_DIRECTORY")
	if mode == "server" {
		serveWitnessNamespace(t, directory)
		return
	}
	content, err := os.ReadFile(filepath.Join(directory, "ready.json"))
	if err != nil {
		t.Fatal(err)
	}
	var info namespaceInfo
	if err := json.Unmarshal(content, &info); err != nil {
		t.Fatal(err)
	}
	if mode == "doctor" {
		report, err := Diagnose(context.Background(), DoctorOptions{Data: filepath.Join(directory, "data")})
		if err != nil {
			t.Fatal(err)
		}
		writeNamespaceJSON(t, filepath.Join(directory, "report.json"), report)
		return
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: doctorTLS(info.Pin)}, Timeout: 5 * time.Second}
	request, _ := http.NewRequest(http.MethodGet, info.Endpoint.Origin()+"/.well-known/space-protocol", nil)
	if mode == "forwarded" {
		request.Header.Set("X-Forwarded-For", "1.1.1.1")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
}

func serveWitnessNamespace(t *testing.T, directory string) {
	data := filepath.Join(directory, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	handler := &namespaceHandler{}
	server := httptest.NewUnstartedServer(handler)
	listener, err := net.Listen("tcp", "11.10.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = server.Listener.Close()
	server.Listener = listener
	server.StartTLS()
	defer server.Close()
	endpoint, err := EndpointFromOrigin(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(server.Certificate().Raw)
	pin := hex.EncodeToString(digest[:])
	id := "srv_" + strings.Repeat("1", 32)
	witness, err := NewNetworkWitness(context.Background(), data, id, pin, []Endpoint{endpoint})
	if err != nil {
		t.Fatal(err)
	}
	defer witness.Close()
	handler.Lock()
	handler.witness = witness
	handler.document = namespaceDiscovery(t, endpoint, pin, id)
	handler.Unlock()
	if err := os.WriteFile(filepath.Join(data, "server.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	writeNamespaceJSON(t, filepath.Join(data, "state.json"), map[string]string{"server_id": id})
	writeNamespaceJSON(t, filepath.Join(directory, "config.json"), NetworkConfig{IP: endpoint.Host, Port: endpoint.Port, Endpoints: []Endpoint{endpoint}, Revision: 1, ListenHost: endpoint.Host, ListenPort: endpoint.Port})
	writeNamespaceJSON(t, filepath.Join(directory, "ready.json"), namespaceInfo{endpoint, pin, id})
	time.Sleep(35 * time.Second)
}

type namespaceHandler struct {
	sync.Mutex
	witness  *NetworkWitness
	document map[string]any
}

func (h *namespaceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.Lock()
	defer h.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.document)
	if h.witness != nil && r.Header.Get("X-Forwarded-For") == "" {
		h.witness.Observe(r.Host, r.RemoteAddr, r.TLS.ServerName, time.Now())
	}
}

func namespaceDiscovery(t *testing.T, endpoint Endpoint, pin, id string) map[string]any {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := EndpointPayload(id, endpoint.Origin(), 1, pin, []Endpoint{endpoint})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"server_id": id, "signing_public_key": base64.RawURLEncoding.EncodeToString(public), "protocol_version": "0.1-experimental", "signing_algorithm": "Ed25519", "identity_origin": endpoint.Origin(), "tls_fingerprint": pin, "endpoints": []Endpoint{endpoint}, "endpoint_revision": 1, "endpoint_signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, payload))}
}

func writeNamespaceJSON(t *testing.T, path string, value any) {
	t.Helper()
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
}
