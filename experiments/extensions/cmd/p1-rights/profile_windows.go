//go:build windows

package main

import (
	"encoding/json"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

func profileDiskProbe(raw []byte) bool {
	var request struct{ Mode, Directory string }
	if json.Unmarshal(raw, &request) != nil || request.Mode != "profile-disk" {
		return false
	}
	path := func(name string) string { return filepath.Join(request.Directory, name) }
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
