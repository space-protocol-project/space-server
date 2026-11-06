//go:build windows

// Package wintoken читает флаги токена через документированный native API.
package wintoken

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var query = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryInformationToken")

// Flag использует DWORD-контракт TOKEN_INFORMATION_CLASS из ntifs.h.
// Ошибка native API никогда не превращается в нулевой флаг.
func Flag(token windows.Token, class uint32) (uint32, error) {
	var value, size uint32
	status, _, _ := query.Call(uintptr(token), uintptr(class), uintptr(unsafe.Pointer(&value)), unsafe.Sizeof(value), uintptr(unsafe.Pointer(&size)))
	if int32(status) < 0 || size != 4 {
		return 0, fmt.Errorf("token class %d: NTSTATUS=%#x size=%d", class, uint32(status), size)
	}
	return value, nil
}
