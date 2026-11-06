//go:build windows

package extensions

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func pinRecoveryProcesses(t *testing.T, pids []uint32) {
	t.Helper()
	for _, pid := range pids {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			t.Fatalf("Удержание worker pid=%d: %v", pid, err)
		}
		t.Cleanup(func() { windows.CloseHandle(handle) })
		requireRecoveryAlive(t, handle, pid)
	}
}

func requireRecoveryAlive(t *testing.T, handle windows.Handle, pid uint32) {
	t.Helper()
	status, err := windows.WaitForSingleObject(handle, 0)
	if err == nil && status == uint32(windows.WAIT_TIMEOUT) {
		return
	}
	var code uint32
	queryError := windows.GetExitCodeProcess(handle, &code)
	t.Fatalf("Worker завершён до аварии supervisor: pid=%d wait=%d err=%v exit=%d query=%v", pid, status, err, code, queryError)
}

func TestWindowsRestrictedRecoveryRepeated(t *testing.T) {
	if os.Getenv("P1_WORKER") == "" {
		t.Skip("Требуется Windows CI")
	}
	for round := 0; round < 25; round++ {
		t.Run(fmt.Sprintf("running-%02d", round), func(t *testing.T) { recoveryCrash(t, "running") })
	}
}

// Контроль жизненного цикла PID: удержанный объект доступен после завершения,
// но его signaled-состояние не должно считаться живым worker.
func TestWindowsRestrictedRecoveryExitedHandle(t *testing.T) {
	if os.Getenv("P1_PRESSURE") == "" {
		t.Skip("Требуется Windows CI")
	}
	cmd := exec.Command(os.Getenv("P1_PRESSURE"), "spin")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pid := uint32(cmd.Process.Pid)
	pinRecoveryProcesses(t, []uint32{pid})
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("Ожидалось аварийное завершение")
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatal("Удержанный объект недоступен", err)
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("Завершение не обнаружено", status, err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil || code == 259 {
		t.Fatal("Нет кода завершения", code, err)
	}
	t.Logf("pinned_pid=%d exited=true exit_code=%d", pid, code)
}
