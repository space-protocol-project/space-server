//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

type pipeRevokeResult struct {
	Read, Write, Reopen accessResult
	ReadBytes           uint32
	PID                 int
	Pending             bool
}

func pipeRevokeProbe(raw []byte) bool {
	var request struct {
		pipeExchangeRequest
		Control string
	}
	if json.Unmarshal(raw, &request) != nil || request.Mode != "pipe-revoke" {
		return false
	}
	run := revokeNamedPipe
	if request.Async {
		run = revokePendingPipe
	}
	result, err := run(request.pipeExchangeRequest, request.Control)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		os.Exit(3)
	}
	return true
}

func revokeNamedPipe(request pipeExchangeRequest, control string) (pipeRevokeResult, error) {
	result := pipeRevokeResult{PID: os.Getpid()}
	data, err := openVerifiedPipe(request)
	if err != nil {
		return result, err
	}
	defer windows.CloseHandle(data)
	name := request.Pipe
	if err := pipeRoundTrip(data); err != nil {
		return result, err
	}
	request.Pipe = control
	other, err := openVerifiedPipe(request)
	if err != nil {
		return result, err
	}
	defer windows.CloseHandle(other)
	// Ответ контрольного канала отправляется только после отключения первого.
	if err := pipeRoundTrip(other); err != nil {
		return result, err
	}
	var buffer [4]byte
	err = windows.ReadFile(data, buffer[:], &result.ReadBytes, nil)
	result.Read = resultOf(err)
	var count uint32
	result.Write = resultOf(windows.WriteFile(data, []byte("late"), &count, nil))
	// Проверяется сам CreateFile; успешное открытие не маскируется отказом proof.
	result.Reopen = resultOf(reopenRevokedPipe(name))
	if err := pipeRoundTrip(other); err != nil {
		return result, err
	}
	return result, nil
}

func reopenRevokedPipe(name string) error {
	pointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_READ_DATA|windows.FILE_WRITE_DATA|windows.SYNCHRONIZE, 0, nil, windows.OPEN_EXISTING, windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err == nil {
		windows.CloseHandle(handle)
	}
	return err
}
