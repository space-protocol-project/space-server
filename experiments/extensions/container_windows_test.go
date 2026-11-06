//go:build windows

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/space-protocol-project/space-server/experiments/extensions/internal/wintoken"
	"golang.org/x/sys/windows"
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
	control, err := NewProcessHost(t.TempDir(), os.Getenv("P1_RIGHTS"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	controlCtx, controlCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer controlCancel()
	baseline, err := control.run(controlCtx, input)
	if err != nil {
		t.Fatalf("control: %s %v", baseline, err)
	}
	var normal map[string]any
	if json.Unmarshal(baseline, &normal) != nil || normal["network_connected"] != true || normal["app_container"] != float64(0) {
		t.Fatalf("control: %s", baseline)
	}
	if err := os.WriteFile(file, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, "created")); err != nil {
		t.Fatal(err)
	}
	t.Logf("control: %s", baseline)
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
	for _, name := range []string{"read_denied", "write_denied", "create_denied"} {
		if result[name] != true {
			t.Fatalf("%s: %s", name, raw)
		}
	}
	if result["network_connected"] != false || result["network_error"] == nil {
		t.Fatal(string(raw))
	}
	if result["app_container"] != float64(1) {
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
			if _, err := h.run(ctx, input); err == nil || errors.Is(err, ErrIsolation) {
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

func TestWindowsRestrictedChildDenied(t *testing.T) {
	h := restrictedHost(t, "P1_PRESSURE")
	input, _ := json.Marshal(workerRequest{Request: []byte(`"child"`)})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := h.run(ctx, input)
	if !errors.Is(err, ErrWorker) {
		t.Fatalf("Ожидался отказ запуска: %s %v", raw, err)
	}
	var result struct {
		Error string
		Code  uint32
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Code != uint32(windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("Неожиданная причина отказа: %s", raw)
	}
	winEmpty(t, h)
	t.Logf("child_spawn_denied=true code=%d", result.Code)
}

func TestWindowsRestrictedPolicy(t *testing.T) {
	h := restrictedHost(t, "P1_WORKER")
	for _, policy := range []uint32{0, 1} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) { checkPackagePolicy(t, h, policy) })
	}
}

func checkPackagePolicy(t *testing.T, h *ProcessHost, policy uint32) {
	t.Helper()
	container := *h.platform.container
	container.policy = policy
	job, err := createJob(WorkerMemory)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	defer stopJob(job)
	pipes, err := newWorkerPipes()
	if err != nil {
		t.Fatal(err)
	}
	defer pipes.close()
	process, err := createContained(h.executable, h.group, [2]windows.Handle{h.platform.job, job}, pipes.child, &container)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	var token windows.Token
	if err := windows.OpenProcessToken(process.Process, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	broad, err := wintoken.PackageAccess(token, "S-1-15-2-1")
	if err != nil || broad != (policy == 0) {
		t.Fatalf("policy=%d broad=%v err=%v", policy, broad, err)
	}
	err = h.platform.container.verify(process.Process)
	if (err == nil) != (policy == 1) {
		t.Fatalf("policy=%d verification=%v", policy, err)
	}
	t.Logf("policy=%d ALL_APPLICATION_PACKAGES=%v", policy, broad)
}
