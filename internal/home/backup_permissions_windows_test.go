package home

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	for _, path := range []string{root, key} {
		descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		text := descriptor.String()
		if !strings.Contains(text, user.User.Sid.String()) || !strings.Contains(text, ";;;SY)") || strings.Contains(text, ";;;WD)") || strings.Contains(text, ";;;BU)") || strings.Contains(text, ";;;AU)") {
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
