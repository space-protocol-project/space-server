//go:build linux

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func processHost(t *testing.T, executable string) *ProcessHost {
	t.Helper()
	root := os.Getenv("P1_CGROUP")
	if root == "" {
		t.Skip("Требуется отдельный привилегированный CI cgroup v2")
	}
	h, err := NewProcessHost(root, os.Getenv(executable))
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

func TestIsolationCompileAndRecover(t *testing.T) {
	h := processHost(t, "P1_WORKER")
	for _, interpreter := range []bool{false, true} {
		for _, name := range []string{"echo", "state", "grow", "start_denied", "host_denied"} {
			for i := 0; i < 2; i++ {
				got, err := h.CompileAndCall(context.Background(), fixture(t, name), []byte(`{"ok":true}`), interpreter, false)
				if err != nil || string(got) != `{"ok":true}` {
					t.Fatalf("%s: %s %v", name, got, err)
				}
			}
		}
		for _, name := range []string{"loop", "alloc_loop", "free_loop", "start_loop", "host_wait", "pointer", "length", "overlap", "bad_json", "unknown_import"} {
			if _, err := h.CompileAndCall(context.Background(), fixture(t, name), []byte(`{}`), interpreter, true); err == nil {
				t.Fatal(name)
			}
		}
	}
	if _, err := h.CompileAndCall(context.Background(), []byte("bad wasm"), []byte(`{}`), false, false); err == nil {
		t.Fatal("Неверный модуль принят")
	}
	healthy(t, h)
	stats := h.Stats()
	if stats.PeakBytes == 0 || stats.Active != 0 || stats.ReservedBytes != 0 {
		t.Fatal(stats)
	}
	t.Logf("worker_memory=%d total_memory=%d stats=%+v", WorkerMemory, TotalMemory, stats)
	emptyGroups(t, h)
}

func healthy(t *testing.T, h *ProcessHost) {
	t.Helper()
	if _, err := h.CompileAndCall(context.Background(), fixture(t, "echo"), []byte(`{}`), false, false); err != nil {
		t.Fatal(err)
	}
}

func emptyGroups(t *testing.T, h *ProcessHost) {
	t.Helper()
	entries, err := os.ReadDir(h.group)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("Не удалена cgroup %s", entry.Name())
		}
	}
}

func TestIsolationPressure(t *testing.T) {
	for _, mode := range []string{"memory", "hang", "cpu", "child", "output", "crash"} {
		t.Run(mode, func(t *testing.T) {
			h := processHost(t, "P1_PRESSURE")
			started := time.Now()
			request, _ := json.Marshal(mode)
			_, err := h.CompileAndCall(context.Background(), []byte("fixture"), request, false, false)
			if err == nil || time.Since(started) > 4*time.Second {
				t.Fatalf("Нет ограниченного отказа: %v", err)
			}
			stats := h.Stats()
			if mode == "cpu" && (stats.CPUUsec == 0 || stats.Throttled == 0) {
				t.Fatalf("Нет ограничения CPU: %+v", stats)
			}
			if stats.Active != 0 || stats.ReservedBytes != 0 {
				t.Fatal(stats)
			}
			emptyGroups(t, h)
			// Тот же supervisor остаётся пригоден после аварии worker.
			h.executable = os.Getenv("P1_WORKER")
			healthy(t, h)
			t.Logf("mode=%s elapsed=%s stats=%+v", mode, time.Since(started), stats)
		})
	}
}

func TestIsolationSharedQuota(t *testing.T) {
	h := processHost(t, "P1_PRESSURE")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := h.CompileAndCall(ctx, []byte("fixture"), []byte(`"hang"`), false, false)
			done <- err
		}()
	}
	waitActive(t, h, 2)
	waitRunning(t, h, 2)
	if _, err := h.CompileAndCall(ctx, []byte("fixture"), []byte(`{}`), false, false); !errors.Is(err, ErrQuota) {
		t.Fatal(err)
	}
	if h.Stats().ReservedBytes != TotalMemory {
		t.Fatal(h.Stats())
	}
	t.Logf("two_running_workers stats=%+v", h.Stats())
	if err := h.Close(); !errors.Is(err, ErrQuota) {
		t.Fatal(err)
	}
	cancel()
	for i := 0; i < 2; i++ {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	emptyGroups(t, h)
	if h.Stats().ReservedBytes != 0 {
		t.Fatal(h.Stats())
	}
	// Закрытый supervisor не принимает новые вызовы.
	if _, err := h.CompileAndCall(context.Background(), []byte("fixture"), []byte(`{}`), false, false); !errors.Is(err, ErrIsolation) {
		t.Fatal(err)
	}
}

func waitRunning(t *testing.T, h *ProcessHost, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		entries, err := os.ReadDir(h.group)
		if err != nil {
			t.Fatal(err)
		}
		running := 0
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			raw, _ := os.ReadFile(filepath.Join(h.group, entry.Name(), "cgroup.events"))
			if strings.Contains(string(raw), "populated 1") {
				running++
			}
		}
		if running == count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Worker не запущены в cgroup")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestIsolationRejectBeforeLaunch(t *testing.T) {
	h := processHost(t, "P1_WORKER")
	for _, input := range [][]byte{nil, make([]byte, MaxModule+1)} {
		if _, err := h.CompileAndCall(context.Background(), input, []byte(`{}`), false, false); !errors.Is(err, ErrQuota) {
			t.Fatal(err)
		}
	}
	if _, err := h.CompileAndCall(context.Background(), []byte("fixture"), []byte(`{"a":1,"a":2}`), false, false); !errors.Is(err, ErrJSON) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.CompileAndCall(ctx, []byte("fixture"), []byte(`{}`), false, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	emptyGroups(t, h)
	if h.Stats().Active != 0 {
		t.Fatal(h.Stats())
	}
}

func waitActive(t *testing.T, h *ProcessHost, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for h.Stats().Active != count {
		if time.Now().After(deadline) {
			t.Fatal("Нет активных вызовов")
		}
		time.Sleep(time.Millisecond)
	}
}

// Квота ядра проверяется независимо от двухсекундного срока публичного вызова.
func TestIsolationKernelWorkerLimit(t *testing.T) {
	h := processHost(t, "P1_PRESSURE")
	input, _ := json.Marshal(workerRequest{Request: []byte(`"memory"`)})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := h.run(ctx, input)
	stats := h.Stats()
	if err == nil || ctx.Err() != nil || stats.OOMKills == 0 {
		t.Fatalf("Не подтверждён OOM worker: err=%v context=%v stats=%+v", err, ctx.Err(), stats)
	}
	t.Logf("worker_fault_injection limit=%d stats=%+v", WorkerMemory, stats)
	emptyGroups(t, h)
	h.executable = os.Getenv("P1_WORKER")
	healthy(t, h)
}

func TestIsolationKernelAggregateLimit(t *testing.T) {
	h := processHost(t, "P1_PRESSURE")
	// Аварийная инъекция обходит admission, проверяя независимую границу ядра ОС.
	// Каждому worker доступно 256 MiB; два раза по 200 MiB превышают общий предел 256 MiB.
	if err := os.WriteFile(filepath.Join(h.group, "memory.max"), []byte("268435456"), 0600); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(workerRequest{Request: []byte(`"global"`)})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := h.run(ctx, input); done <- err }()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err == nil {
			t.Fatal("Нет отказа по памяти")
		}
	}
	stats := h.Stats()
	if ctx.Err() != nil || stats.OOMKills == 0 {
		t.Fatalf("Общая квота не сработала: %+v", stats)
	}
	t.Logf("aggregate_fault_injection limit=268435456 stats=%+v", stats)
	emptyGroups(t, h)
}

func TestIsolationIdentityAndEnvironment(t *testing.T) {
	h := processHost(t, "P1_PRESSURE")
	t.Setenv("P1_SECRET_SENTINEL", "must-not-reach-worker")
	got, err := h.CompileAndCall(context.Background(), []byte("fixture"), []byte(`"identity"`), false, false)
	if err != nil || !strings.Contains(string(got), `"uid":65534`) || !strings.Contains(string(got), `"environment":2`) {
		t.Fatalf("%s %v", got, err)
	}
	t.Log(string(got))
}
