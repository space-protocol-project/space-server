//go:build windows

package extensions

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func recoveryJobPIDs(t *testing.T, job windows.Handle) []uint32 {
	t.Helper()
	var info struct {
		Assigned, Count uint32
		IDs             [64]uintptr
	}
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		t.Fatal(err)
	}
	if info.Count > uint32(len(info.IDs)) {
		t.Fatal("Переполнение списка процессов")
	}
	var pids []uint32
	for _, id := range info.IDs[:info.Count] {
		pids = append(pids, uint32(id))
	}
	return pids
}

func recoveryWorkerImage(t *testing.T, pid uint32, executable string) bool {
	t.Helper()
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	// Снимок Job может содержать уже завершающийся вспомогательный conhost.
	if err == windows.ERROR_INVALID_PARAMETER {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	var path [32768]uint16
	size := uint32(len(path))
	if err := windows.QueryFullProcessImageName(handle, 0, &path[0], &size); err != nil {
		t.Fatal(err)
	}
	return strings.EqualFold(windows.UTF16ToString(path[:size]), executable)
}

func waitRecoveryWorkers(t *testing.T, h *ProcessHost) []uint32 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var workers []uint32
		pids := recoveryJobPIDs(t, h.platform.job)
		for _, pid := range pids {
			if recoveryWorkerImage(t, pid, h.executable) {
				workers = append(workers, pid)
			}
		}
		if len(workers) == 2 {
			t.Logf("worker_images=2 job_processes=%d", len(pids))
			return workers
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Нет двух настоящих worker в Job")
	return nil
}

func pinRecoveryProcesses(t *testing.T, pids []uint32, executable string) {
	t.Helper()
	for _, pid := range pids {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			t.Fatalf("Удержание worker pid=%d: %v", pid, err)
		}
		t.Cleanup(func() { windows.CloseHandle(handle) })
		requireRecoveryAlive(t, handle, pid)
		var path [32768]uint16
		size := uint32(len(path))
		if err := windows.QueryFullProcessImageName(handle, 0, &path[0], &size); err != nil {
			t.Fatal(err)
		}
		image := windows.UTF16ToString(path[:size])
		if !strings.EqualFold(image, executable) {
			t.Fatalf("Неожиданный процесс pid=%d image=%s expected=%s", pid, image, executable)
		}
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
	pinRecoveryProcesses(t, []uint32{pid}, cmd.Path)
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
