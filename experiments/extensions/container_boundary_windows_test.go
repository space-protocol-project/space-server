//go:build windows

package extensions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func boundaryInput(t *testing.T, request any) []byte {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(workerRequest{Request: raw})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func boundaryRun(t *testing.T, h *ProcessHost, request, result any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := h.run(ctx, boundaryInput(t, request))
	decodeBoundary(t, h, output, err, result)
}

func decodeBoundary(t *testing.T, h *ProcessHost, output []byte, err error, result any) {
	t.Helper()
	if err != nil {
		t.Fatalf("probe: %s %v", output, err)
	}
	if err := json.Unmarshal(output, result); err != nil {
		t.Fatal(err)
	}
	t.Log(string(output))
	winEmpty(t, h)
}

// Положительный контроль заменяет только разрешённый stderr в тестовом запуске.
// Обычный запуск по-прежнему передаёт исключительно три канала worker.
func runWithFileHandle(t *testing.T, h *ProcessHost, handle windows.Handle, input []byte, allowed bool) ([]byte, error) {
	t.Helper()
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
	handles := pipes.child
	if allowed {
		handles[2] = handle
	}
	process, err := createContained(h.executable, h.group, [2]windows.Handle{h.platform.job, job}, handles, h.platform.container)
	pipes.closeChild()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	if err := h.platform.container.verify(process.Process); err != nil {
		t.Fatal(err)
	}
	var copied windows.Handle
	copyErr := windows.DuplicateHandle(process.Process, handle, windows.CurrentProcess(), &copied, 0, false, windows.DUPLICATE_SAME_ACCESS)
	if copyErr == nil {
		windows.CloseHandle(copied)
	}
	if !allowed {
		if copyErr != windows.ERROR_INVALID_HANDLE {
			t.Fatalf("Посторонний handle: %v", copyErr)
		}
		if err := stopJob(job); err != nil {
			t.Fatal(err)
		}
		return []byte(`{"same_file":false,"query":{"allowed":false,"code":6}}`), nil
	}
	if copyErr != nil {
		t.Fatal("Разрешённый handle отсутствует", copyErr)
	}
	previous, err := windows.ResumeThread(process.Thread)
	if err != nil || previous != 1 {
		t.Fatal("ResumeThread", previous, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return boundaryExchange(t, ctx, process.Process, job, pipes, input)
}

func boundaryExchange(t *testing.T, ctx context.Context, process, job windows.Handle, pipes *workerPipes, input []byte) ([]byte, error) {
	t.Helper()
	stdout, stderr := &cappedOutput{limit: MaxJSON}, &cappedOutput{limit: 4096}
	stop := func() { _ = windows.TerminateJobObject(job, 1) }
	stdout.onOverflow, stderr.onOverflow = stop, stop
	done := pipes.exchange(input, stdout, stderr)
	err := waitProcess(ctx, process)
	if failure := stopJob(job); failure != nil {
		t.Fatal(failure)
	}
	pipeError := drainPipes(done)
	if len(stderr.data) != 0 {
		t.Logf("worker stderr: %s", stderr.data)
	}
	if pipeError != nil {
		t.Fatal(pipeError)
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, ErrQuota
	}
	return stdout.data, err
}

func TestWindowsRestrictedHandleAllowlist(t *testing.T) {
	h := restrictedHost(t, "P1_RIGHTS")
	file, err := os.Create(filepath.Join(t.TempDir(), "private-handle"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	handle := windows.Handle(file.Fd())
	if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		t.Fatal(err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"Mode": "handle", "Handle": uintptr(handle), "Volume": info.VolumeSerialNumber, "High": info.FileIndexHigh, "Low": info.FileIndexLow}
	for _, allowed := range []bool{false, true, false} {
		var result struct {
			SameFile bool           `json:"same_file"`
			Query    resourceResult `json:"query"`
		}
		output, err := runWithFileHandle(t, h, handle, boundaryInput(t, request), allowed)
		decodeBoundary(t, h, output, err, &result)
		if result.SameFile != allowed {
			t.Fatalf("allowlisted=%v result=%+v", allowed, result)
		}
		if allowed && (!result.Query.Allowed || result.Query.Code != 0) {
			t.Fatal(result)
		}
		t.Logf("allowlisted=%v same_file=%v", allowed, result.SameFile)
	}
}

func resetProfileFiles(t *testing.T, folder string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(folder, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(folder, "created")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func checkProfileFiles(t *testing.T, h *ProcessHost, folder string, allowed bool) {
	t.Helper()
	var results map[string]resourceResult
	boundaryRun(t, h, map[string]string{"Mode": "files", "File": filepath.Join(folder, "secret"), "Directory": folder}, &results)
	if len(results) != 3 {
		t.Fatal(results)
	}
	for _, name := range []string{"read", "write", "create"} {
		result, exists := results[name]
		code := uint32(5)
		if allowed {
			code = 0
		}
		if !exists || result.Allowed != allowed || result.Code != code {
			t.Fatalf("%s: %+v", name, result)
		}
	}
	if !allowed {
		assertProfileUnchanged(t, folder)
	}
}

func assertProfileUnchanged(t *testing.T, folder string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(folder, "secret"))
	if err != nil || string(data) != "secret" {
		t.Fatal("Чужой файл изменён", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "created")); !os.IsNotExist(err) {
		t.Fatal("Создан чужой файл", err)
	}
}

func TestWindowsRestrictedSeparateProfiles(t *testing.T) {
	a, b := restrictedHost(t, "P1_RIGHTS"), restrictedHost(t, "P1_RIGHTS")
	if a.platform.container.sid.String() == b.platform.container.sid.String() {
		t.Fatal("Совпадают SID")
	}
	first, second := containerFolder(t, a.platform.container), containerFolder(t, b.platform.container)
	resetProfileFiles(t, first)
	resetProfileFiles(t, second)
	checkProfileFiles(t, a, first, true)
	checkProfileFiles(t, b, second, true)
	resetProfileFiles(t, first)
	resetProfileFiles(t, second)
	checkProfileFiles(t, a, second, false)
	checkProfileFiles(t, b, first, false)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("Первый профиль не удалён", err)
	}
	checkProfileFiles(t, b, second, true)
	t.Log("distinct_sids=true cross_read_write_create=denied surviving_profile=true")
}
