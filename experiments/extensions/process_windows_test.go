//go:build windows

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func windowsHost(t *testing.T, name string) *ProcessHost {
	t.Helper()
	executable := os.Getenv(name)
	if executable == "" {
		t.Skip("Требуется отдельный Windows CI с worker")
	}
	h, err := NewProcessHost(t.TempDir(), executable)
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

func winHealthy(t *testing.T, h *ProcessHost) {
	t.Helper()
	got, err := h.CompileAndCall(context.Background(), fixture(t, "echo"), []byte(`{"ok":true}`), false, false)
	if err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("%s %v", got, err)
	}
}

func winEmpty(t *testing.T, h *ProcessHost) {
	t.Helper()
	info, err := accounting(h.platform.job)
	if err != nil || info.ActiveProcesses != 0 {
		t.Fatalf("Job не пуст: %+v %v", info, err)
	}
	if stats := h.Stats(); stats.Active != 0 || stats.ReservedBytes != 0 {
		t.Fatal(stats)
	}
}

func TestWindowsIsolationWASM(t *testing.T) {
	h := windowsHost(t, "P1_WORKER")
	for _, interpreter := range []bool{false, true} {
		for _, name := range []string{"echo", "state", "grow", "start_denied", "host_denied"} {
			for i := 0; i < 2; i++ {
				got, err := h.CompileAndCall(context.Background(), fixture(t, name), []byte(`{}`), interpreter, false)
				if err != nil || string(got) != `{}` {
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
	winHealthy(t, h)
	winEmpty(t, h)
	if h.Stats().PeakBytes == 0 {
		t.Fatal(h.Stats())
	}
	t.Logf("WASM stats=%+v", h.Stats())
}

func TestWindowsIsolationLimits(t *testing.T) {
	h := windowsHost(t, "P1_WORKER")
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(h.platform.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		t.Fatal(err)
	}
	if info.JobMemoryLimit != TotalMemory || info.BasicLimitInformation.ActiveProcessLimit != 64 {
		t.Fatal(info)
	}
	if info.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		t.Fatal("Нет kill-on-close")
	}
	var cpu struct{ Flags, Rate uint32 }
	if err := windows.QueryInformationJobObject(h.platform.job, windows.JobObjectCpuRateControlInformation, uintptr(unsafe.Pointer(&cpu)), uint32(unsafe.Sizeof(cpu)), nil); err != nil {
		t.Fatal(err)
	}
	if cpu.Flags != 5 || cpu.Rate != 5000 {
		t.Fatal(cpu)
	}
	t.Logf("memory=%d active_limit=%d cpu_flags=%d cpu_rate=%d", info.JobMemoryLimit, info.BasicLimitInformation.ActiveProcessLimit, cpu.Flags, cpu.Rate)
}

func TestWindowsIsolationPressure(t *testing.T) {
	for _, mode := range []string{"memory", "hang", "cpu", "child", "output", "crash"} {
		t.Run(mode, func(t *testing.T) {
			h := windowsHost(t, "P1_PRESSURE")
			request, _ := json.Marshal(mode)
			started := time.Now()
			_, err := h.CompileAndCall(context.Background(), []byte("fixture"), request, false, false)
			elapsed := time.Since(started)
			if err == nil || elapsed > 4*time.Second {
				t.Fatalf("%s: %v", elapsed, err)
			}
			if mode == "memory" && errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("Ожидался отказ выделения памяти до deadline")
			}
			stats := h.Stats()
			if mode == "memory" && stats.PeakBytes < 128<<20 {
				t.Fatal(stats)
			}
			if mode == "cpu" && stats.CPUUsec == 0 {
				t.Fatal(stats)
			}
			winEmpty(t, h)
			h.executable = os.Getenv("P1_WORKER")
			winHealthy(t, h)
			t.Logf("mode=%s elapsed=%s stats=%+v", mode, elapsed, stats)
		})
	}
}

func waitWindowsProcesses(t *testing.T, h *ProcessHost, count uint32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for h.Stats().OSProcesses != count {
		if time.Now().After(deadline) {
			t.Fatalf("Нет %d процессов: %+v", count, h.Stats())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWindowsIsolationSharedQuota(t *testing.T) {
	h := windowsHost(t, "P1_PRESSURE")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := h.CompileAndCall(ctx, []byte("fixture"), []byte(`"hang"`), false, false)
			done <- err
		}()
	}
	waitWindowsProcesses(t, h, 2)
	if h.Stats().ReservedBytes != TotalMemory {
		t.Fatal(h.Stats())
	}
	if _, err := h.CompileAndCall(ctx, []byte("fixture"), []byte(`{}`), false, false); !errors.Is(err, ErrQuota) {
		t.Fatal(err)
	}
	t.Logf("two_workers=%+v", h.Stats())
	if err := h.Close(); !errors.Is(err, ErrQuota) {
		t.Fatal(err)
	}
	cancel()
	for i := 0; i < 2; i++ {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	winEmpty(t, h)
	if _, err := h.CompileAndCall(context.Background(), []byte("fixture"), []byte(`{}`), false, false); !errors.Is(err, ErrIsolation) {
		t.Fatal(err)
	}
}

type memoryProbeResult struct {
	Allocated int
	Denied    bool
	Code      uint32
}

func checkMemoryDenial(t *testing.T, result memoryProbeResult) {
	t.Helper()
	if !result.Denied || result.Allocated < 128<<20 || result.Allocated >= WorkerMemory {
		t.Fatalf("Нет отказа VirtualAlloc: %+v", result)
	}
	if result.Code != 8 && result.Code != 1455 && result.Code != 1816 {
		t.Fatalf("Не ошибка памяти: %+v", result)
	}
}

func TestWindowsIsolationNativeMemory(t *testing.T) {
	h := windowsHost(t, "P1_PRESSURE")
	request, _ := json.Marshal(map[string]int{"NativeMemory": 384})
	raw, err := h.CompileAndCall(context.Background(), []byte("fixture"), request, false, false)
	if err != nil {
		t.Fatal(err)
	}
	var result memoryProbeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	checkMemoryDenial(t, result)
	winEmpty(t, h)
	t.Logf("native_memory=%+v stats=%+v", result, h.Stats())
}

func namedEvent(t *testing.T) (windows.Handle, string) {
	t.Helper()
	name := fmt.Sprintf("Local\\space-p1-%d-%d", os.Getpid(), time.Now().UnixNano())
	pointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	event, err := windows.CreateEvent(nil, 1, 0, pointer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(event) })
	return event, name
}

func TestWindowsIsolationAggregateMemory(t *testing.T) {
	h := windowsHost(t, "P1_PRESSURE")
	if err := setJobLimit(h.platform.job, WorkerMemory); err != nil {
		t.Fatal(err)
	}
	release, releaseName := namedEvent(t)
	var ready [2]windows.Handle
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	type outcome struct {
		result memoryProbeResult
		err    error
	}
	done := make(chan outcome, 2)
	received := 0
	launched := 0
	defer func() {
		cancel()
		for received < launched {
			<-done
			received++
		}
	}()
	for i := 0; i < 2; i++ {
		event, name := namedEvent(t)
		ready[i] = event
		request, _ := json.Marshal(map[string]any{"NativeMemory": 208, "Ready": name, "Release": releaseName})
		input, _ := json.Marshal(workerRequest{Request: request})
		launched++
		go func() {
			raw, err := h.run(ctx, input)
			var result memoryProbeResult
			if err == nil {
				err = json.Unmarshal(raw, &result)
			}
			done <- outcome{result, err}
		}()
	}
	for _, event := range ready {
		status, err := windows.WaitForSingleObject(event, 2000)
		if err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatalf("Нет двух готовых процессов: %v %d", err, status)
		}
	}
	if err := windows.SetEvent(release); err != nil {
		t.Fatal(err)
	}
	denied := 0
	for received < 2 {
		out := <-done
		received++
		if out.err != nil {
			t.Fatal(out.err)
		}
		if out.result.Denied {
			denied++
			if out.result.Code != 8 && out.result.Code != 1455 && out.result.Code != 1816 {
				t.Fatal(out.result)
			}
		}
		t.Logf("aggregate_worker=%+v", out.result)
	}
	if denied == 0 {
		t.Fatal("Общая квота не отклонила VirtualAlloc")
	}
	winEmpty(t, h)
	t.Logf("aggregate_limit=%d denied=%d stats=%+v", WorkerMemory, denied, h.Stats())
}

func TestWindowsIsolationAssignmentFailure(t *testing.T) {
	h := windowsHost(t, "P1_PRESSURE")
	job := h.platform.job
	h.platform.job = 0
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	request, _ := json.Marshal(map[string]string{"Marker": marker})
	_, err := h.CompileAndCall(context.Background(), []byte("fixture"), request, false, false)
	h.platform.job = job
	if !errors.Is(err, ErrIsolation) {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Код исполнился до назначения Job", err)
	}
	winEmpty(t, h)
	h.executable = os.Getenv("P1_WORKER")
	winHealthy(t, h)
}

func TestWindowsIsolationEnvironment(t *testing.T) {
	h := windowsHost(t, "P1_PRESSURE")
	t.Setenv("P1_SECRET_SENTINEL", "must-not-reach-worker")
	got, err := h.CompileAndCall(context.Background(), []byte("fixture"), []byte(`"identity"`), false, false)
	if err != nil || !strings.Contains(string(got), `"environment":3`) || !strings.Contains(string(got), `"secret":""`) {
		t.Fatalf("%s %v", got, err)
	}
	t.Log(string(got))
}
