//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"os"
	"path/filepath"
	"unsafe"
)

func profileDiskProbe(raw []byte) bool {
	var request struct{ Mode, Directory string }
	if json.Unmarshal(raw, &request) != nil || (request.Mode != "profile-disk" && request.Mode != "profile-root") {
		return false
	}
	path := func(name string) string { return filepath.Join(request.Directory, name) }
	if request.Mode == "profile-root" {
		result := map[string]accessResult{
			"create":                 resultOf(nativeFile(path("root-created"), true, windows.CREATE_NEW)),
			"mkdir":                  resultOf(os.Mkdir(path("root-directory"), 0700)),
			"dacl":                   resultOf(changeProfileDACL(request.Directory)),
			"private_registry_write": resultOf(writePrivateRegistry()),
		}
		if json.NewEncoder(os.Stdout).Encode(result) != nil {
			os.Exit(3)
		}
		return true
	}
	result := map[string]accessResult{
		"read":           resultOf(nativeFile(path("secret"), false, windows.OPEN_EXISTING)),
		"write":          resultOf(nativeFile(path("write"), true, windows.OPEN_EXISTING)),
		"append":         resultOf(appendProfileFile(path("append"))),
		"truncate":       resultOf(os.Truncate(path("truncate"), 1)),
		"create":         resultOf(nativeFile(path("created"), true, windows.CREATE_NEW)),
		"mkdir":          resultOf(os.Mkdir(path("new-directory"), 0700)),
		"delete":         resultOf(os.Remove(path("delete"))),
		"rename":         resultOf(os.Rename(path("rename"), path("renamed"))),
		"hardlink":       resultOf(os.Link(path("secret"), path("linked"))),
		"dacl":           resultOf(changeProfileDACL(path("acl"))),
		"directory_dacl": resultOf(changeProfileDACL(request.Directory)),
	}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		os.Exit(3)
	}
	return true
}

func writePrivateRegistry() error {
	var key registry.Key
	result, _, _ := windows.NewLazySystemDLL("userenv.dll").NewProc("GetAppContainerRegistryLocation").Call(uintptr(registry.SET_VALUE), uintptr(unsafe.Pointer(&key)))
	if int32(result) < 0 {
		if uint32(result)&0xffff0000 == 0x80070000 {
			return windows.Errno(uint32(result) & 0xffff)
		}
		return fmt.Errorf("GetAppContainerRegistryLocation: %#x", result)
	}
	defer key.Close()
	return key.SetStringValue("SpaceP1Boundary", "probe")
}

func appendProfileFile(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(make([]byte, 1<<20))
	return err
}

func changeProfileDACL(path string) error {
	sd, err := windows.SecurityDescriptorFromString("D:(A;OICI;FA;;;WD)")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
