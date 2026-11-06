//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

type pendingPipeIO struct {
	handle    windows.Handle
	operation windows.Overlapped
	buffer    []byte
	started   error
}

func beginPipeIO(handle windows.Handle, buffer []byte, write bool) (*pendingPipeIO, error) {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	p := &pendingPipeIO{handle: handle, buffer: buffer, operation: windows.Overlapped{HEvent: event}}
	var count uint32
	if write {
		p.started = windows.WriteFile(handle, buffer, &count, &p.operation)
	} else {
		p.started = windows.ReadFile(handle, buffer, &count, &p.operation)
	}
	return p, nil
}

func (p *pendingPipeIO) close() {
	if p.started == windows.ERROR_IO_PENDING {
		_ = windows.CancelIoEx(p.handle, &p.operation)
		var count uint32
		_ = windows.GetOverlappedResult(p.handle, &p.operation, &count, true)
	}
	runtime.KeepAlive(p.buffer)
	windows.CloseHandle(p.operation.HEvent)
}

func (p *pendingPipeIO) finish() (uint32, error) {
	if p.started != nil && p.started != windows.ERROR_IO_PENDING {
		return 0, p.started
	}
	state, err := windows.WaitForSingleObject(p.operation.HEvent, 5000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		return 0, fmt.Errorf("Ожидание I/O: state=%d err=%v", state, err)
	}
	var count uint32
	err = windows.GetOverlappedResult(p.handle, &p.operation, &count, false)
	runtime.KeepAlive(p.buffer)
	return count, err
}

func asyncPipeIO(handle windows.Handle, buffer []byte, write bool) (uint32, error) {
	p, err := beginPipeIO(handle, buffer, write)
	if err != nil {
		return 0, err
	}
	defer p.close()
	return p.finish()
}

func asyncPipeRoundTrip(handle windows.Handle) error {
	if count, err := asyncPipeIO(handle, []byte("ping"), true); err != nil || count != 4 {
		return fmt.Errorf("Запрос: count=%d err=%v", count, err)
	}
	var reply [4]byte
	if count, err := asyncPipeIO(handle, reply[:], false); err != nil || count != 4 || string(reply[:]) != "pong" {
		return fmt.Errorf("Ответ: count=%d err=%v", count, err)
	}
	return nil
}

func revokePendingPipe(request pipeExchangeRequest, control string) (pipeRevokeResult, error) {
	result := pipeRevokeResult{PID: os.Getpid()}
	data, err := openVerifiedPipe(request)
	if err != nil {
		return result, err
	}
	defer windows.CloseHandle(data)
	if err := asyncPipeRoundTrip(data); err != nil {
		return result, err
	}
	pending, err := beginPipeIO(data, make([]byte, 4), false)
	if err != nil {
		return result, err
	}
	defer pending.close()
	var count uint32
	if pending.started != windows.ERROR_IO_PENDING || windows.GetOverlappedResult(data, &pending.operation, &count, false) != windows.ERROR_IO_INCOMPLETE {
		return result, fmt.Errorf("Чтение не находится в ожидании")
	}
	result.Pending = true
	name := request.Pipe
	request.Pipe, request.Async = control, false
	other, err := openVerifiedPipe(request)
	if err != nil {
		return result, err
	}
	defer windows.CloseHandle(other)
	// Сервер отключает канал только после этого подтверждения ожидания.
	if err := pipeRoundTrip(other); err != nil {
		return result, err
	}
	result.ReadBytes, err = pending.finish()
	result.Read = resultOf(err)
	_, err = asyncPipeIO(data, []byte("late"), true)
	result.Write = resultOf(err)
	result.Reopen = resultOf(reopenRevokedPipe(name))
	if err := pipeRoundTrip(other); err != nil {
		return result, err
	}
	return result, nil
}
