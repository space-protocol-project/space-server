//go:build windows

package main

import (
	"encoding/json"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// Прямой VirtualAlloc отделяет отказ ОС от поведения Go при нехватке памяти.
func nativeProbe(raw []byte) bool {
	var request struct {
		NativeMemory   int
		Ready, Release string
	}
	if json.Unmarshal(raw, &request) != nil || request.NativeMemory == 0 {
		return false
	}
	const chunk = 16 << 20
	var blocks []uintptr
	defer func() {
		for _, block := range blocks {
			_ = windows.VirtualFree(block, 0, windows.MEM_RELEASE)
		}
	}()
	result := struct {
		Allocated int
		Denied    bool
		Code      uint32
	}{}
	for result.Allocated < request.NativeMemory<<20 {
		block, err := windows.VirtualAlloc(0, chunk, windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
		if err != nil {
			result.Denied = true
			if code, ok := err.(syscall.Errno); ok {
				result.Code = uint32(code)
			}
			break
		}
		blocks = append(blocks, block)
		result.Allocated += chunk
		if result.Allocated == 64<<20 && request.Ready != "" {
			if !memoryBarrier(request.Ready, request.Release) {
				os.Exit(5)
			}
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
	return true
}

func memoryBarrier(readyName, releaseName string) bool {
	readyString, err := windows.UTF16PtrFromString(readyName)
	if err != nil {
		return false
	}
	ready, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, readyString)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(ready)
	releaseString, err := windows.UTF16PtrFromString(releaseName)
	if err != nil {
		return false
	}
	release, err := windows.OpenEvent(windows.SYNCHRONIZE, false, releaseString)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(release)
	if windows.SetEvent(ready) != nil {
		return false
	}
	status, err := windows.WaitForSingleObject(release, 5000)
	return err == nil && status == windows.WAIT_OBJECT_0
}
