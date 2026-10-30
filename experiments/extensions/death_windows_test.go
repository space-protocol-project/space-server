//go:build windows

package extensions

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsIsolationDeathHelper(t *testing.T) {
	marker := os.Getenv("P1_DEATH_MARKER")
	if marker == "" {
		t.Skip("Вспомогательный процесс аварийного теста")
	}
	h := windowsHost(t, "P1_PRESSURE")
	job, err := createJob(WorkerMemory)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	pipes, err := newWorkerPipes()
	if err != nil {
		t.Fatal(err)
	}
	defer pipes.close()
	process, err := createAssigned(h.executable, h.group, [2]windows.Handle{h.platform.job, job}, pipes.child)
	pipes.closeChild()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	for _, handle := range []windows.Handle{h.platform.job, job} {
		info, err := accounting(handle)
		if err != nil || info.ActiveProcesses != 1 {
			t.Fatal("Процесс не принадлежит обоим Job до ResumeThread", info, err)
		}
	}
	count := 1
	if os.Getenv("P1_DEATH_STAGE") == "running" {
		input, _ := json.Marshal(workerRequest{Request: []byte(`"child"`)})
		pipes.exchange(input, &cappedOutput{limit: MaxJSON}, &cappedOutput{limit: 4096})
		if _, err := windows.ResumeThread(process.Thread); err != nil {
			t.Fatal(err)
		}
		count = 2
	}
	pids := waitJobPIDs(t, job, count)
	data, _ := json.Marshal(pids)
	if err := os.WriteFile(marker, data, 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Minute)
}

func waitJobPIDs(t *testing.T, job windows.Handle, count int) []uint32 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var info struct {
			Assigned, Count uint32
			IDs             [64]uintptr
		}
		err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
		if err != nil {
			t.Fatal(err)
		}
		if int(info.Count) == count {
			var result []uint32
			for _, id := range info.IDs[:count] {
				result = append(result, uint32(id))
			}
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Нет ожидаемого дерева процессов")
	return nil
}

func TestWindowsIsolationSupervisorDeath(t *testing.T) {
	if os.Getenv("P1_PRESSURE") == "" {
		t.Skip("Требуется отдельный Windows CI")
	}
	for _, stage := range []string{"suspended", "running"} {
		t.Run(stage, func(t *testing.T) { supervisorDeath(t, stage) })
	}
}

func supervisorDeath(t *testing.T, stage string) {
	marker := filepath.Join(t.TempDir(), "pids.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsIsolationDeathHelper$", "-test.timeout=70s")
	cmd.Env = append(os.Environ(), "P1_DEATH_MARKER="+marker, "P1_DEATH_STAGE="+stage)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pids := waitDeathMarker(t, marker)
	var handles []windows.Handle
	for _, pid := range pids {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { windows.CloseHandle(handle) })
		handles = append(handles, handle)
	}
	started := time.Now()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("Supervisor не был принудительно завершён")
	}
	for _, handle := range handles {
		status, err := windows.WaitForSingleObject(handle, 2000)
		if err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatal("Старый процесс пережил supervisor", status, err)
		}
	}
	h := windowsHost(t, "P1_WORKER")
	winHealthy(t, h)
	winEmpty(t, h)
	t.Logf("stage=%s terminated=%d healthy=true elapsed=%s", stage, len(handles), time.Since(started))
}

func waitDeathMarker(t *testing.T, marker string) []uint32 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker)
		var pids []uint32
		if err == nil && json.Unmarshal(data, &pids) == nil && len(pids) != 0 {
			return pids
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Нет контрольного списка процессов")
	return nil
}

func TestWindowsIsolationInvalidChildJob(t *testing.T) {
	h := windowsHost(t, "P1_WORKER")
	pipes, err := newWorkerPipes()
	if err != nil {
		t.Fatal(err)
	}
	defer pipes.close()
	process, err := createAssigned(h.executable, h.group, [2]windows.Handle{h.platform.job, 0}, pipes.child)
	if err == nil {
		windows.TerminateProcess(process.Process, 1)
		windows.CloseHandle(process.Thread)
		windows.CloseHandle(process.Process)
		t.Fatal("Создание с неверным дочерним Job прошло")
	}
	winEmpty(t, h)
	winHealthy(t, h)
}
