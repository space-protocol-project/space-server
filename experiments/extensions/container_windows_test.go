//go:build windows

package extensions

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func restrictedHost(t *testing.T, name string) *ProcessHost {
	t.Helper()
	executable := os.Getenv(name)
	if executable == "" {
		t.Skip("Требуется Windows CI с worker")
	}
	h, err := NewWindowsRestrictedHost(t.TempDir(), executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.Close(); err != nil {
			t.Error(err)
		}
	})
	return h
}

func TestWindowsRestrictedWASM(t *testing.T) {
	h := restrictedHost(t, "P1_WORKER")
	for _, interpreter := range []bool{false, true} {
		got, err := h.CompileAndCall(context.Background(), fixture(t, "echo"), []byte(`{"ok":true}`), interpreter, false)
		if err != nil || string(got) != `{"ok":true}` {
			t.Fatalf("%s %v", got, err)
		}
	}
	winEmpty(t, h)
	directory := h.group
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("Снимок не удалён", err)
	}
}

func TestWindowsRestrictedRights(t *testing.T) {
	h := restrictedHost(t, "P1_RIGHTS")
	directory := t.TempDir()
	file := filepath.Join(directory, "secret")
	if err := os.WriteFile(file, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// Контроль: endpoint доступен родителю, это не отказ отсутствующего сервера.
	connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	request, _ := json.Marshal(map[string]string{"File": file, "Directory": directory, "Address": listener.Addr().String()})
	input, _ := json.Marshal(workerRequest{Request: request})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := h.run(ctx, input)
	if err != nil {
		t.Fatalf("%s %v", raw, err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read_denied", "write_denied", "create_denied", "network_denied"} {
		if result[name] != true {
			t.Fatalf("%s: %s", name, raw)
		}
	}
	if result["app_container"] != float64(1) || result["lpac"] != float64(1) {
		t.Fatal(string(raw))
	}
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "secret" {
		t.Fatal("Изменён файл родителя", err)
	}
	winEmpty(t, h)
	t.Log(string(raw))
}

func TestWindowsRestrictedPressure(t *testing.T) {
	for _, mode := range []string{"memory", "child", "output", "crash"} {
		t.Run(mode, func(t *testing.T) {
			h := restrictedHost(t, "P1_PRESSURE")
			request, _ := json.Marshal(mode)
			input, _ := json.Marshal(workerRequest{Request: request})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := h.run(ctx, input); err == nil {
				t.Fatal("Нет отказа нагрузки")
			}
			winEmpty(t, h)
		})
	}
}

func TestWindowsRestrictedMemory(t *testing.T) {
	h := restrictedHost(t, "P1_PRESSURE")
	input, _ := json.Marshal(workerRequest{Request: []byte(`{"NativeMemory":384}`)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := h.run(ctx, input)
	if err != nil {
		t.Fatalf("%s %v", raw, err)
	}
	var result struct {
		Allocated int
		Denied    bool
		Code      uint32
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Denied || result.Code == 0 || result.Allocated == 0 || result.Allocated >= WorkerMemory {
		t.Fatal(string(raw))
	}
	winEmpty(t, h)
	t.Log(string(raw))
}

func TestWindowsRestrictedTree(t *testing.T) {
	h := restrictedHost(t, "P1_PRESSURE")
	input, _ := json.Marshal(workerRequest{Request: []byte(`"child"`)})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := h.run(ctx, input); done <- err }()
	pids := waitJobPIDs(t, h.platform.job, 2)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("Отмена не доставлена")
	}
	winEmpty(t, h)
	t.Logf("Остановлены worker и потомок: %v", pids)
}
