//go:build windows

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func pipeEvent(t *testing.T) windows.Overlapped {
	t.Helper()
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(event) })
	return windows.Overlapped{HEvent: event}
}

func finishPipeIO(handle windows.Handle, operation *windows.Overlapped, started error) (uint32, error) {
	if started != nil && started != windows.ERROR_IO_PENDING {
		return 0, started
	}
	if started == windows.ERROR_IO_PENDING {
		state, err := windows.WaitForSingleObject(operation.HEvent, 5000)
		if err != nil || state != windows.WAIT_OBJECT_0 {
			_ = windows.CancelIoEx(handle, operation)
			var count uint32
			// OVERLAPPED и буфер живут до подтверждения завершения отменённого I/O.
			_ = windows.GetOverlappedResult(handle, operation, &count, true)
			return 0, fmt.Errorf("Ожидание pipe: state=%d err=%v", state, err)
		}
	}
	var count uint32
	err := windows.GetOverlappedResult(handle, operation, &count, false)
	return count, err
}

func connectProfilePipe(t *testing.T, pipe windows.Handle) error {
	t.Helper()
	operation := pipeEvent(t)
	err := windows.ConnectNamedPipe(pipe, &operation)
	if err == windows.ERROR_PIPE_CONNECTED {
		return nil
	}
	_, err = finishPipeIO(pipe, &operation, err)
	return err
}

func profilePipeIO(t *testing.T, pipe windows.Handle, data []byte, write bool) (uint32, error) {
	t.Helper()
	operation := pipeEvent(t)
	var count uint32
	var err error
	if write {
		err = windows.WriteFile(pipe, data, &count, &operation)
	} else {
		err = windows.ReadFile(pipe, data, &count, &operation)
	}
	count, err = finishPipeIO(pipe, &operation, err)
	runtime.KeepAlive(data)
	return count, err
}

func verifyPipeClient(h *ProcessHost, pipe windows.Handle) (uint32, error) {
	var pid uint32
	ok, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetNamedPipeClientProcessId").Call(uintptr(pipe), uintptr(unsafe.Pointer(&pid)))
	if ok == 0 {
		return 0, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(process)
	var inJob int32
	ok, _, err = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob").Call(uintptr(process), uintptr(h.platform.job), uintptr(unsafe.Pointer(&inJob)))
	if ok == 0 {
		return 0, err
	}
	if inJob == 0 {
		return 0, ErrIsolation
	}
	if err := h.platform.container.verify(process); err != nil {
		return 0, err
	}
	return pid, nil
}

type pipeWorkerResult struct {
	output []byte
	err    error
}

func startPipeClient(t *testing.T, h *ProcessHost, name string) (context.CancelFunc, <-chan pipeWorkerResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	input := boundaryInput(t, map[string]string{"Mode": "pipe-exchange", "Pipe": name})
	done := make(chan pipeWorkerResult, 1)
	go func() { output, err := h.run(ctx, input); done <- pipeWorkerResult{output, err} }()
	return cancel, done
}

func awaitPipeClient(t *testing.T, done <-chan pipeWorkerResult) pipeWorkerResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("Worker не завершился")
		return pipeWorkerResult{}
	}
}

func profileExchange(t *testing.T, h, other *ProcessHost, cancelCall bool) {
	t.Helper()
	f := newProfileIPC(t, h)
	pipe := f.openServerFlags(t, windows.FILE_FLAG_OVERLAPPED)
	defer windows.CloseHandle(pipe)
	cancel, done := startPipeClient(t, h, f.pipeLocal)
	defer cancel()
	if err := connectProfilePipe(t, pipe); err != nil {
		cancel()
		result := awaitPipeClient(t, done)
		t.Fatalf("ConnectNamedPipe: %v; worker=%s err=%v", err, result.output, result.err)
	}
	pid, err := verifyPipeClient(h, pipe)
	if err != nil {
		t.Fatal("Клиент не подтверждён", err)
	}
	if _, err := verifyPipeClient(other, pipe); err == nil {
		t.Fatal("Клиент принят чужим хостом")
	}
	var request [4]byte
	count, err := profilePipeIO(t, pipe, request[:], false)
	if err != nil || count != 4 || string(request[:]) != "ping" {
		t.Fatal("Неверный запрос", count, err)
	}
	if cancelCall {
		cancelProfileExchange(t, h, pipe, cancel, done)
	} else {
		completeProfileExchange(t, h, pipe, done)
	}
	t.Logf("authenticated_pid=%d wrong_host_rejected=true cancelled=%v", pid, cancelCall)
}

func completeProfileExchange(t *testing.T, h *ProcessHost, pipe windows.Handle, done <-chan pipeWorkerResult) {
	t.Helper()
	count, err := profilePipeIO(t, pipe, []byte("pong"), true)
	if err != nil || count != 4 {
		t.Fatal("Не отправлен ответ", count, err)
	}
	result := awaitPipeClient(t, done)
	if result.err != nil {
		t.Fatal(result.err)
	}
	var response resourceResult
	if err := json.Unmarshal(result.output, &response); err != nil {
		t.Fatal(err)
	}
	if !response.Allowed || response.Code != 0 {
		t.Fatal(string(result.output))
	}
	winEmpty(t, h)
}

func cancelProfileExchange(t *testing.T, h *ProcessHost, pipe windows.Handle, cancel context.CancelFunc, done <-chan pipeWorkerResult) {
	t.Helper()
	var data [1]byte
	operation := pipeEvent(t)
	var count uint32
	started := windows.ReadFile(pipe, data[:], &count, &operation)
	if started != windows.ERROR_IO_PENDING {
		t.Fatal("Чтение не перешло в ожидание", started)
	}
	cancel()
	count, err := finishPipeIO(pipe, &operation, started)
	runtime.KeepAlive(data)
	result := awaitPipeClient(t, done)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatal("Нет отмены", result.err)
	}
	if count != 0 || !errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		t.Fatal("Канал не закрылся", count, err)
	}
	winEmpty(t, h)
}

func TestWindowsRestrictedPipeExchange(t *testing.T) {
	a, b := restrictedHost(t, "P1_RIGHTS"), restrictedHost(t, "P1_RIGHTS")
	t.Run("exchange-a", func(t *testing.T) { profileExchange(t, a, b, false) })
	t.Run("exchange-b", func(t *testing.T) { profileExchange(t, b, a, false) })
	t.Run("cancel-a", func(t *testing.T) { profileExchange(t, a, b, true) })
	t.Run("recover-a", func(t *testing.T) { profileExchange(t, a, b, false) })
	t.Run("surviving-b", func(t *testing.T) { profileExchange(t, b, a, false) })
}
