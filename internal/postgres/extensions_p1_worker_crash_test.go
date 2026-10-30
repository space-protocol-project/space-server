//go:build linux

package postgres

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
)

type extensionCrashRecorder struct{ extensions.Runtime }

func (h extensionCrashRecorder) Invoke(ctx context.Context, call extensions.Invocation) ([]byte, error) {
	file, err := os.OpenFile(os.Getenv("SPACE_P1_CRASH_CALLS"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if _, err = file.WriteString("invoke\n"); err != nil {
		return nil, err
	}
	if err = file.Sync(); err != nil {
		return nil, err
	}
	return h.Runtime.Invoke(ctx, call)
}

func extensionCrashRuntime(t *testing.T) (extensions.Runtime, []byte) {
	t.Helper()
	worker := os.Getenv("SPACE_P1_CRASH_WORKER")
	if worker == "" {
		return extensionCrashRecorder{&mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}}, []byte{1}
	}
	host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), worker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	module, err := os.ReadFile(os.Getenv("SPACE_P1_MUTATION_MODULE"))
	if err != nil {
		t.Fatal(err)
	}
	return extensionCrashRecorder{host}, module
}

func TestExtensionP1MutationWorkerCrashRecovery(t *testing.T) {
	if os.Getenv("SPACE_P1_CRASH_STOP_WORKER") == "" {
		t.Skip("Нужен изолированный CI с аварийным worker")
	}
	for _, mode := range []string{"during_worker", "before_commit", "after_commit"} {
		t.Run(mode, func(t *testing.T) {
			// Выполняется после kill дочернего сервера: новый host убирает оставшиеся cgroup.
			t.Cleanup(func() {
				host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), os.Getenv("SPACE_P1_WORKER"))
				if err != nil {
					t.Error(err)
					return
				}
				if err := host.Close(); err != nil {
					t.Error(err)
				}
				groups, err := filepath.Glob(filepath.Join(os.Getenv("SPACE_P1_CGROUP"), "space-p1-*"))
				if err != nil || len(groups) != 0 {
					t.Error("Остались cgroup", groups, err)
				}
			})
			testExtensionCrashRecovery(t, mode, true)
		})
	}
}

func extensionCrashEnvironment(t *testing.T, wasm bool, mode string) []string {
	t.Helper()
	if !wasm {
		return nil
	}
	worker := os.Getenv("SPACE_P1_WORKER")
	if mode == "during_worker" {
		worker = os.Getenv("SPACE_P1_CRASH_STOP_WORKER")
	}
	if worker == "" {
		t.Fatal("Не задан worker")
	}
	return []string{"SPACE_P1_CRASH_WORKER=" + worker}
}

func waitExtensionStoppedWorker(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		files, err := filepath.Glob(filepath.Join(os.Getenv("SPACE_P1_CGROUP"), "space-p1-*", "call-*", "cgroup.procs"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			for _, pid := range strings.Fields(string(data)) {
				status, err := os.ReadFile(filepath.Join("/proc", pid, "status"))
				if err == nil && strings.Contains(string(status), "State:\tT (stopped)") {
					t.Logf("Настоящий WASM worker pid=%s остановлен до выдачи результата", pid)
					return
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Живой остановленный worker не обнаружен")
}

func waitExtensionWorkerEmpty(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		files, err := filepath.Glob(filepath.Join(os.Getenv("SPACE_P1_CGROUP"), "space-p1-*", "cgroup.events"))
		if err != nil {
			t.Fatal(err)
		}
		empty := true
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil || !strings.Contains(string(data), "populated 0") {
				empty = false
			}
		}
		if empty {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Worker остался жив после SIGKILL сервера")
}
