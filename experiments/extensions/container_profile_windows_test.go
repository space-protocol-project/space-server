//go:build windows

package extensions

import (
	"os"
	"path/filepath"
	"testing"
)

var profileFiles = []string{"secret", "write", "append", "truncate", "delete", "rename", "acl"}

func prepareProfileProbe(t *testing.T, folder string) {
	t.Helper()
	for _, name := range profileFiles {
		if err := os.WriteFile(filepath.Join(folder, name), []byte("secret"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func profileProbe(t *testing.T, h *ProcessHost, folder string, writable bool) {
	t.Helper()
	var results map[string]resourceResult
	boundaryRun(t, h, map[string]string{"Mode": "profile-disk", "Directory": folder}, &results)
	if len(results) != 11 {
		t.Fatal(results)
	}
	for _, name := range []string{"read", "write", "append", "truncate", "create", "mkdir", "delete", "rename", "hardlink", "dacl", "directory_dacl"} {
		result := results[name]
		allowed := writable || name == "read"
		expectedCode := uint32(5)
		if allowed {
			expectedCode = 0
		}
		if result.Allowed != allowed || result.Code != expectedCode {
			t.Fatalf("%s: %+v", name, result)
		}
	}
	if writable {
		return
	}
	for _, name := range profileFiles {
		bytes, err := os.ReadFile(filepath.Join(folder, name))
		if err != nil || string(bytes) != "secret" {
			t.Fatalf("Changed %s: %q %v", name, bytes, err)
		}
	}
	entries, err := os.ReadDir(folder)
	if err != nil || len(entries) != len(profileFiles) {
		t.Fatalf("Changed directory: %v %v", entries, err)
	}
}

func TestWindowsRestrictedProfileReadOnly(t *testing.T) {
	h := restrictedHost(t, "P1_RIGHTS")
	control, err := NewProcessHost(t.TempDir(), os.Getenv("P1_RIGHTS"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	baseline := t.TempDir()
	prepareProfileProbe(t, baseline)
	profileProbe(t, control, baseline, true)
	folder := containerFolder(t, h.platform.container)
	root, err := h.platform.container.profilePath()
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{root, folder, h.group} {
		var results map[string]resourceResult
		boundaryRun(t, h, map[string]string{"Mode": "profile-root", "Directory": target}, &results)
		if len(results) != 4 {
			t.Fatal(results)
		}
		for _, result := range results {
			if result.Allowed || result.Code != 5 {
				t.Fatal(results)
			}
		}
	}
	for _, name := range []string{"files", filepath.Join("nested", "files")} {
		target := filepath.Join(folder, name)
		if err := os.MkdirAll(target, 0700); err != nil {
			t.Fatal(err)
		}
		prepareProfileProbe(t, target)
		profileProbe(t, h, target, false)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatalf("Profile not removed: %v", err)
	}
}
