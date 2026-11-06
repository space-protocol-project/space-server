//go:build windows

package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type pipeExchangeRequest struct {
	Mode, Pipe               string
	ServerPID, ServerSession uint32
	Nonce                    []byte
	Async                    bool
}

func pipeExchangeProbe(raw []byte) bool {
	var request pipeExchangeRequest
	if json.Unmarshal(raw, &request) != nil || request.Mode != "pipe-exchange" {
		return false
	}
	err := exchangeNamedPipe(request)
	if json.NewEncoder(os.Stdout).Encode(resultOf(err)) != nil {
		os.Exit(3)
	}
	return true
}

func exchangeNamedPipe(request pipeExchangeRequest) error {
	handle, err := openVerifiedPipe(request)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return pipeRoundTrip(handle)
}

func openVerifiedPipe(request pipeExchangeRequest) (windows.Handle, error) {
	if request.ServerPID == 0 || len(request.Nonce) != 16 {
		return 0, windows.ERROR_ACCESS_DENIED
	}
	pointer, err := windows.UTF16PtrFromString(request.Pipe)
	if err != nil {
		return 0, err
	}
	access := uint32(windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.SYNCHRONIZE)
	flags := uint32(windows.SECURITY_SQOS_PRESENT | windows.SECURITY_IDENTIFICATION)
	if request.Async {
		flags |= windows.FILE_FLAG_OVERLAPPED
	}
	handle, err := windows.CreateFile(pointer, access, 0, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return 0, err
	}
	if err := verifyPipeServer(handle, request); err != nil {
		windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func pipeRoundTrip(handle windows.Handle) error {
	var count uint32
	if err := windows.WriteFile(handle, []byte("ping"), &count, nil); err != nil {
		return err
	}
	if count != 4 {
		return fmt.Errorf("Неполный запрос")
	}
	var reply [4]byte
	if err := windows.ReadFile(handle, reply[:], &count, nil); err != nil {
		return err
	}
	if count != 4 || string(reply[:]) != "pong" {
		return fmt.Errorf("Неверный ответ")
	}
	return nil
}

func verifyPipeServer(handle windows.Handle, request pipeExchangeRequest) error {
	for name, expected := range map[string]uint32{"GetNamedPipeServerProcessId": request.ServerPID, "GetNamedPipeServerSessionId": request.ServerSession} {
		var actual uint32
		ok, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc(name).Call(uintptr(handle), uintptr(unsafe.Pointer(&actual)))
		if ok == 0 {
			return err
		}
		if actual != expected {
			return windows.ERROR_ACCESS_DENIED
		}
	}
	var nonce [16]byte
	var count uint32
	var err error
	if request.Async {
		count, err = asyncPipeIO(handle, nonce[:], false)
	} else {
		err = windows.ReadFile(handle, nonce[:], &count, nil)
	}
	if err != nil {
		return err
	}
	if count != uint32(len(nonce)) || subtle.ConstantTimeCompare(nonce[:], request.Nonce) != 1 {
		return windows.ERROR_ACCESS_DENIED
	}
	return nil
}
