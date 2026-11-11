package packagep1_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageIsolatedLimits(t *testing.T) {
	if os.Getenv("SPACE_P1_VERIFY_ISOLATION") != "1" {
		t.Skip("Только изолированная отрицательная матрица Linux")
	}
	if os.Getuid() == 0 {
		t.Fatal("Проверка работает с UID 0")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(status), "NoNewPrivs:\t1") {
		t.Fatal("Нет NoNewPrivileges")
	}
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "0::/") || strings.Contains(line, "\n") {
		t.Fatal("Не cgroup v2", line)
	}
	root := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line, "0::/"))
	for file, want := range map[string]string{"memory.max": "134217728", "memory.swap.max": "0", "pids.max": "32"} {
		value, err := os.ReadFile(filepath.Join(root, file))
		if err != nil || strings.TrimSpace(string(value)) != want {
			t.Fatal(file, string(value), err)
		}
		t.Log(file, strings.TrimSpace(string(value)))
	}
	cpu, err := os.ReadFile(filepath.Join(root, "cpu.max"))
	if err != nil {
		t.Fatal(err)
	}
	values := strings.Fields(string(cpu))
	if len(values) != 2 || values[0] != values[1] || values[0] == "max" {
		t.Fatal("Нет квоты CPU 100%", string(cpu))
	}
	t.Log("cpu.max", strings.TrimSpace(string(cpu)), "NoNewPrivs=1 nonroot=true")
}
