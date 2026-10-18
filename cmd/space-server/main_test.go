package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestLocalOrigin(t *testing.T) {
	cases := []struct{ name, address, override, want string }{
		{"default", "127.0.0.1:8080", "", "http://127.0.0.1:8080"},
		{"override", "127.0.0.1:8080", "http://127.0.0.1:80/", "http://127.0.0.1"},
		{"ipv6", "127.0.0.1:8080", "http://[::1]:80/", "http://[::1]"},
		{"external", "127.0.0.1:8080", "http://8.8.8.8", ""},
		{"https", "127.0.0.1:8080", "https://127.0.0.1", ""},
		{"path", "127.0.0.1:8080", "http://127.0.0.1/chat", ""},
		{"credentials", "127.0.0.1:8080", "http://user@127.0.0.1", ""},
		{"query", "127.0.0.1:8080", "http://127.0.0.1?secret=x", ""},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got, err := localOrigin(item.address, item.override)
			if (err != nil) != (item.want == "") || got != item.want {
				t.Fatalf("origin=%q error=%v want=%q", got, err, item.want)
			}
		})
	}
}

func TestMaintenanceRejectsConflictingOperations(t *testing.T) {
	cases := []serverOptions{
		{portableVerify: "archive.zip", portableImport: "archive.zip", portableTo: "new"},
		{portableVerify: "archive.zip", homeMigrate: true},
		{portableVerify: "archive.zip", backup: "backup"},
		{portableVerify: "archive.zip", firstOwner: true},
		{homeCheck: true, homeMigrate: true, homeData: "data", homeRuntime: "runtime"},
		{homeStopped: true, homeCheck: true, homeData: "data", homeRuntime: "runtime"},
		{backup: "backup", restore: "backup", homeRuntime: "runtime"},
		{backup: "backup", homeRuntime: "runtime"},
		{restore: "backup", homeRuntime: "runtime"},
		{runtimeInfo: true, checkDatabase: true},
	}
	for _, options := range cases {
		handled, err := performMaintenance(options)
		if !handled || err == nil {
			t.Fatalf("expected rejection for %+v, handled=%t error=%v", options, handled, err)
		}
	}
	handled, err := performMaintenance(serverOptions{})
	if handled || err != nil {
		t.Fatalf("ordinary startup handled=%t error=%v", handled, err)
	}
}

func TestWriteHomeStateReplacesExistingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeHomeState(path, "http://127.0.0.1:8080", "https://8.8.8.8:8443", "setup"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]string
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state) != 3 || state["local_origin"] != "http://127.0.0.1:8080" || state["public_origin"] != "https://8.8.8.8:8443" || state["setup_code"] != "setup" {
		t.Fatalf("invalid state: %v", state)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary state remains: %v", err)
	}
}

func TestPublicRequestsRequireInitializedOwner(t *testing.T) {
	var initialized atomic.Bool
	calls := 0
	handler := requireHomeOwner(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		response.WriteHeader(http.StatusNoContent)
	}), &initialized)
	request := httptest.NewRequest(http.MethodPost, "/space.v1.ContentService/Create", nil)
	blocked := httptest.NewRecorder()
	handler.ServeHTTP(blocked, request)
	if blocked.Code != http.StatusServiceUnavailable || calls != 0 {
		t.Fatalf("status=%d calls=%d", blocked.Code, calls)
	}
	initialized.Store(true)
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, request)
	if allowed.Code != http.StatusNoContent || calls != 1 {
		t.Fatalf("status=%d calls=%d", allowed.Code, calls)
	}
}

func TestLocalListenerAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "[::1]:8080", "127.2.3.4:0"} {
		if err := validateLocalAddresses(serverOptions{port: address, grpcAddress: address}); err != nil {
			t.Errorf("loopback %q: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", "[::]:8080", "8.8.8.8:8080", "localhost:8080", "127.0.0.1", ":8080"} {
		for _, options := range []serverOptions{
			{port: address, grpcAddress: "127.0.0.1:9090"},
			{port: "127.0.0.1:8080", grpcAddress: address},
		} {
			if err := validateLocalAddresses(options); err == nil {
				t.Errorf("accepted invalid address %q", address)
			}
		}
	}
}

func TestHomeRuntimeRequiresDataDirectory(t *testing.T) {
	for _, options := range []serverOptions{{publicIP: "8.8.8.8"}, {homeRuntime: "runtime"}} {
		_, _, _, err := prepareHomeRuntime(options)
		if err == nil {
			t.Fatal("accepted home options without home-data")
		}
	}
	_, origin, stop, err := prepareHomeRuntime(serverOptions{})
	if err != nil || origin != "" || stop() != nil {
		t.Fatalf("ordinary startup origin=%q error=%v", origin, err)
	}
}
