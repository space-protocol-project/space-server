//go:build linux

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Запускается только дочерним процессом теста аварийного восстановления.
func TestIsolationSupervisorHelper(t *testing.T) {
	marker := os.Getenv("P1_RECOVERY_MARKER")
	if marker == "" {
		t.Skip("Вспомогательный процесс аварийного теста")
	}
	h, err := NewProcessHost(os.Getenv("P1_CGROUP"), os.Getenv("P1_PRESSURE"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := os.WriteFile(marker, []byte(h.group), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	input, _ := json.Marshal(workerRequest{Request: []byte(`"child"`)})
	// Длинный срок — только аварийный стенд; публичный deadline остаётся 2 с.
	_, _ = h.run(ctx, input)
}

func TestIsolationSupervisorRecovery(t *testing.T) {
	root := os.Getenv("P1_CGROUP")
	if root == "" {
		t.Skip("Требуется привилегированный CI cgroup v2")
	}
	marker := filepath.Join(t.TempDir(), "group")
	cmd := exec.Command(os.Args[0], "-test.run=^TestIsolationSupervisorHelper$", "-test.timeout=40s")
	cmd.Env = append(os.Environ(), "P1_RECOVERY_MARKER="+marker)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	group := waitSupervisorGroup(t, marker)
	waitRecoveryProcesses(t, group)
	if other, err := NewProcessHost(root, os.Getenv("P1_WORKER")); other != nil || !errors.Is(err, ErrIsolation) {
		t.Fatalf("Второй supervisor получил root: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("Supervisor завершился без принудительного отказа")
	}
	events, err := os.ReadFile(filepath.Join(group, "cgroup.events"))
	if err != nil || !strings.Contains(string(events), "populated 1") {
		t.Fatal("Стенд не оставил живого потомка для восстановления", err)
	}
	started := time.Now()
	h := processHost(t, "P1_WORKER")
	if _, err := os.Stat(group); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Старая cgroup не удалена", err)
	}
	healthy(t, h)
	emptyGroups(t, h)
	t.Logf("supervisor_killed=true orphan_populated=true old_group_removed=true healthy=true recovery=%s", time.Since(started))
}

func waitSupervisorGroup(t *testing.T, marker string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker)
		if err == nil && len(data) != 0 {
			return string(data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Supervisor не создал контрольный файл")
	return ""
}

func waitRecoveryProcesses(t *testing.T, group string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		// pids.current включает потоки; cgroup.procs ниже считает именно процессы.
		entries, err := os.ReadDir(group)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() && countGroupProcesses(filepath.Join(group, entry.Name())) >= 2 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Worker и его потомок не запущены")
}

func countGroupProcesses(group string) int {
	raw, err := os.ReadFile(filepath.Join(group, "cgroup.procs"))
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(raw)))
}

func TestIsolationRecoveryScope(t *testing.T) {
	h := processHost(t, "P1_WORKER")
	root := os.Getenv("P1_CGROUP")
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(root, "unrelated")
	if err := os.Mkdir(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(foreign) })
	stale := filepath.Join(root, "space-p1-interrupted")
	if err := os.Mkdir(stale, 0700); err != nil {
		t.Fatal(err)
	}
	unrecognized := filepath.Join(stale, "unexpected")
	if err := os.Mkdir(unrecognized, 0700); err != nil {
		t.Fatal(err)
	}
	if next, err := NewProcessHost(root, os.Getenv("P1_WORKER")); next != nil || !errors.Is(err, ErrIsolation) {
		t.Fatal("Неизвестная структура принята", err)
	}
	if err := os.Remove(unrecognized); err != nil {
		t.Fatal("Неизвестная группа изменена", err)
	}
	next := processHost(t, "P1_WORKER")
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("Посторонняя группа изменена", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Прерванная инициализация не восстановлена", err)
	}
	healthy(t, next)
}
