//go:build windows

package extensions

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (c *appContainer) profilePath() (string, error) {
	sid, err := windows.UTF16PtrFromString(c.sid.String())
	if err != nil {
		return "", err
	}
	var path *uint16
	result, _, _ := userenv.NewProc("GetAppContainerFolderPath").Call(uintptr(unsafe.Pointer(sid)), uintptr(unsafe.Pointer(&path)))
	if int32(result) < 0 {
		return "", fmt.Errorf("GetAppContainerFolderPath: %#x", result)
	}
	defer windows.NewLazySystemDLL("ole32.dll").NewProc("CoTaskMemFree").Call(uintptr(unsafe.Pointer(path)))
	folder := filepath.Clean(windows.UTF16PtrToString(path))
	expected := filepath.Join(os.Getenv("LOCALAPPDATA"), "Packages", windows.UTF16PtrToString(c.name))
	if !filepath.IsAbs(folder) || !strings.EqualFold(folder, expected) {
		return "", ErrIsolation
	}
	return folder, nil
}

func (c *appContainer) readOnlyACL() (*windows.ACL, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	// OW отменяет неявное право владельца менять DACL. Запись, удаление,
	// WRITE_DAC/WRITE_OWNER запрещены SID гостя даже при унаследованном allow.
	sddl := fmt.Sprintf("D:P(D;OICI;0x000d0156;;;%s)(A;OICI;RC;;;OW)(A;OICI;FA;;;%s)(A;OICI;FA;;;SY)(A;OICI;FRFX;;;%s)", c.sid.String(), user.User.Sid.String(), c.sid.String())
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	acl, _, err := sd.DACL()
	return acl, err
}

func setContainerACL(path string, acl *windows.ACL) error {
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func (c *appContainer) sealProfile() error {
	folder, err := c.profilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(folder, 0700); err != nil {
		return err
	}
	acl, err := c.readOnlyACL()
	if err != nil {
		return err
	}
	count := 0
	return filepath.WalkDir(folder, func(path string, entry fs.DirEntry, failure error) error {
		if failure != nil {
			return failure
		}
		count++
		if count > 1024 {
			return ErrIsolation
		}
		if err := plainPath(path); err != nil {
			return err
		}
		return setContainerACL(path, acl)
	})
}
