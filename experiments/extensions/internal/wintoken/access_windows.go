//go:build windows

// Package wintoken проверяет конкретные права приостановленного worker.
package wintoken

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var accessCheck = windows.NewLazySystemDLL("advapi32.dll").NewProc("AccessCheck")

func Flag(token windows.Token, class uint32) (uint32, error) {
	var value, size uint32
	err := windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(&value)), 4, &size)
	return value, err
}

// PackageAccess проверяет разрешение одного тестового бита через заданный SID.
// Это проверка семантики ACL, а не недоступный на стенде TokenIsLPAC.
func PackageAccess(token windows.Token, sid string) (bool, error) {
	var duplicate windows.Token
	if err := windows.DuplicateTokenEx(token, windows.TOKEN_QUERY, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &duplicate); err != nil {
		return false, err
	}
	defer duplicate.Close()
	sd, err := windows.SecurityDescriptorFromString("O:SYG:SYD:(A;;0x1;;;WD)(A;;0x1;;;" + sid + ")")
	if err != nil {
		return false, err
	}
	mapping := [4]uint32{1, 1, 1, 1}
	var privileges [1024]byte
	size := uint32(len(privileges))
	var granted, allowed uint32
	result, _, failure := accessCheck.Call(uintptr(unsafe.Pointer(sd)), uintptr(duplicate), 1, uintptr(unsafe.Pointer(&mapping[0])), uintptr(unsafe.Pointer(&privileges[0])), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&granted)), uintptr(unsafe.Pointer(&allowed)))
	if result == 0 {
		return false, fmt.Errorf("AccessCheck: %w", failure)
	}
	return allowed != 0 && granted&1 != 0, nil
}
