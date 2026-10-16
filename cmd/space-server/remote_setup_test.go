package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/home"
)

func TestSetupHeaderRejectsExpiredAndDuplicateCodes(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-Space-Setup", "secret")
	if validSetupHeader(r, "secret", time.Now().Add(-time.Second)) {
		t.Fatal("Просроченный код принят")
	}
	r.Header.Add("X-Space-Setup", "secret")
	if validSetupHeader(r, "secret", time.Now().Add(time.Minute)) {
		t.Fatal("Двойной заголовок принят")
	}
}

type setupReader struct{ initialized bool }

func (s *setupReader) SealHomeSetup(context.Context) error { return nil }

func (s *setupReader) GetSetupStatus(context.Context, *pb.GetSetupStatusRequest) (*pb.GetSetupStatusResponse, error) {
	return &pb.GetSetupStatusResponse{Initialized: s.initialized}, nil
}

func TestRemoteSetupGate(t *testing.T) {
	store := &setupReader{}
	ready := &atomic.Bool{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Space-Setup") != "" {
			t.Error("Секрет передан внутреннему обработчику")
		}
		w.WriteHeader(204)
	})
	gate := remoteSetupHandler(context.Background(), next, ready, store, homeHTTPConfig{setupCode: "secret"})
	for _, test := range []struct {
		method, path, code string
		want               int
	}{
		{"GET", "/.well-known/space-protocol", "", 204},
		{"POST", rpc.AuthServiceCreateChallengeProcedure, "", 503},
		{"POST", rpc.AuthServiceCreateChallengeProcedure, "wrong", 503},
		{"POST", rpc.AuthServiceCreateChallengeProcedure, "secret", 204},
		{"GET", rpc.AdminServiceClaimOwnerProcedure, "secret", 503},
		{"POST", rpc.AdminServiceUpdateSettingsProcedure, "secret", 503},
		{"POST", rpc.AdminServiceClaimOwnerProcedure, "secret", 204},
	} {
		r := httptest.NewRequest(test.method, test.path, nil)
		if test.code != "" {
			r.Header.Set("X-Space-Setup", test.code)
		}
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Fatalf("%s %s: %d", test.method, test.path, w.Code)
		}
	}
	store.initialized = true
	r := httptest.NewRequest("POST", rpc.AdminServiceGetSettingsProcedure, nil)
	r.Header.Set("X-Space-Setup", "secret")
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	if w.Code != 204 || !ready.Load() {
		t.Fatal("Владелец не получил немедленный доступ")
	}
	w = httptest.NewRecorder()
	gate.ServeHTTP(w, httptest.NewRequest("POST", rpc.AdminServiceGetSettingsProcedure, nil))
	if w.Code != 204 {
		t.Fatal("Обычный вход после настройки закрыт")
	}
}

func TestStandaloneNetworkPersistsAddress(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := standaloneNetwork(data, "https://space.example.org:9443", "[::]:8443"); err != nil {
		t.Fatal(err)
	}
	config, err := home.ReadNetworkConfig(data)
	if err != nil || config.Endpoints[0].Port != 9443 || config.ListenPort != 8443 || config.ListenHost != "::" {
		t.Fatalf("%+v %v", config, err)
	}
	if err := standaloneNetwork(data, "", "0.0.0.0:8443"); err != nil {
		t.Fatal(err)
	}
	if err := standaloneNetwork(data, "https://other.example.org:9443", "0.0.0.0:8443"); err == nil {
		t.Fatal("Существующая конфигурация заменена")
	}
}
