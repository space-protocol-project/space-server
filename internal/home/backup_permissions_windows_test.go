package home

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestBackupDirectoryProtectsRootAndChildren(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backup")
	if err := publishDirectory(root, func(staging string) error {
		return os.WriteFile(filepath.Join(staging, "server.key"), []byte("fixture"), 0600)
	}); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "server.key")
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, key} {
		descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		dacl, _, err := descriptor.DACL()
		if err != nil || dacl == nil || dacl.AceCount != 2 {
			t.Fatalf("Ожидались ровно два разрешения доступа: %s", path)
		}
		ownerFound, systemFound := false, false
		for index := uint32(0); index < uint32(dacl.AceCount); index++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(dacl, index, &ace); err != nil {
				t.Fatal(err)
			}
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != 0x1f01ff {
				t.Fatalf("Ожидалось разрешение полного доступа: %s", path)
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			ownerFound = ownerFound || sid.Equals(user.User.Sid)
			systemFound = systemFound || sid.Equals(system)
			if path == root && ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE {
				t.Fatal("Права корня не наследуются файлами и каталогами копии")
			}
		}
		if !ownerFound || !systemFound {
			t.Fatalf("Права копии не ограничены владельцем и SYSTEM: %s", path)
		}
		if path == root {
			control, _, err := descriptor.Control()
			if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
				t.Fatal("Корень наследует права родительского каталога")
			}
		}
	}
}
