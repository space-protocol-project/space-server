//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type accessResult struct {
	Allowed bool   `json:"allowed"`
	Code    uint32 `json:"code"`
	Error   string `json:"error,omitempty"`
}

func resultOf(err error) accessResult {
	if err == nil {
		return accessResult{Allowed: true}
	}
	var code syscall.Errno
	_ = errors.As(err, &code)
	return accessResult{Code: uint32(code), Error: err.Error()}
}

func resourceProbe(raw []byte) bool {
	var request struct {
		Mode, Registry, Event, Pipe string
		Process                     uint32
	}
	if json.Unmarshal(raw, &request) != nil || request.Mode != "resources" {
		return false
	}
	results := map[string]accessResult{
		"registry_read":   resultOf(readRegistry(request.Registry)),
		"registry_write":  resultOf(writeRegistry(request.Registry)),
		"registry_create": resultOf(createRegistry(request.Registry)),
		"event":           resultOf(signalEvent(request.Event)),
		"pipe":            resultOf(openPipe(request.Pipe)),
	}
	for name, access := range map[string]uint32{"process_read": windows.PROCESS_VM_READ, "process_duplicate": windows.PROCESS_DUP_HANDLE, "process_terminate": windows.PROCESS_TERMINATE} {
		handle, err := windows.OpenProcess(access, false, request.Process)
		if err == nil {
			windows.CloseHandle(handle)
		}
		results[name] = resultOf(err)
	}
	if json.NewEncoder(os.Stdout).Encode(results) != nil {
		os.Exit(3)
	}
	return true
}

func readRegistry(path string) error {
	key, err := registry.OpenKey(registry.USERS, path, registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	value, _, err := key.GetStringValue("sentinel")
	if err != nil {
		return err
	}
	if value != "secret" {
		return fmt.Errorf("unexpected sentinel")
	}
	return nil
}

func writeRegistry(path string) error {
	key, err := registry.OpenKey(registry.USERS, path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue("sentinel", "tampered")
}

func createRegistry(path string) error {
	key, _, err := registry.CreateKey(registry.USERS, path+`\child`, registry.SET_VALUE)
	if err == nil {
		key.Close()
	}
	return err
}

func signalEvent(name string) error {
	pointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	handle, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, pointer)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return windows.SetEvent(handle)
}

func openPipe(name string) error {
	pointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	return windows.CloseHandle(handle)
}
