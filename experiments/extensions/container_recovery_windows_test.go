//go:build windows

package extensions

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsRestrictedRecoveryHelper(t *testing.T) {
	root := os.Getenv("P1_LPAC_ROOT")
	if root == "" {
		t.Skip("Вспомогательный процесс аварийного теста")
	}
	stage := os.Getenv("P1_LPAC_STAGE")
	var c *appContainer
	var err error
	pids := []uint32{uint32(os.Getpid())}
	if stage == "running" {
		h, failure := NewWindowsRestrictedHost(root, os.Getenv("P1_PRESSURE"))
		if failure != nil {
			t.Fatal(failure)
		}
		c = h.platform.container
		input, _ := json.Marshal(workerRequest{Request: []byte(`"child"`)})
		go func() { _, _ = h.run(context.Background(), input) }()
		pids = append(pids, waitJobPIDs(t, h.platform.job, 2)...)
	} else {
		c, err = beginContainer(root)
		if err != nil {
			t.Fatal(err)
		}
		if stage != "intent" {
			if err := c.create(); err != nil {
				t.Fatal(err)
			}
		}
		if stage == "staged" || stage == "cleanup" {
			if err := c.stage(root, os.Getenv("P1_WORKER")); err != nil {
				t.Fatal(err)
			}
		}
		if stage == "cleanup" {
			profileSentinel(t, c)
			result, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(c.name)))
			if int32(result) < 0 {
				t.Fatal(result)
			}
		}
	}
	if stage != "intent" && stage != "cleanup" {
		profileSentinel(t, c)
	}
	data, _ := json.Marshal(pids)
	if err := os.WriteFile(os.Getenv("P1_LPAC_MARKER"), data, 0600); err != nil {
		t.Fatal(err)
	}
	// Проверяется завершение процесса без defer Close и без кооперативного shutdown.
	time.Sleep(time.Minute)
}

func TestWindowsRestrictedRecovery(t *testing.T) {
	if os.Getenv("P1_WORKER") == "" {
		t.Skip("Требуется Windows CI")
	}
	for _, stage := range []string{"intent", "profile", "staged", "running", "cleanup"} {
		t.Run(stage, func(t *testing.T) { recoveryCrash(t, stage) })
	}
}

func recoveryCrash(t *testing.T, stage string) {
	t.Helper()
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "pids.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsRestrictedRecoveryHelper$", "-test.timeout=70s")
	cmd.Env = append(os.Environ(), "P1_LPAC_ROOT="+root, "P1_LPAC_STAGE="+stage, "P1_LPAC_MARKER="+marker)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pids := waitDeathMarker(t, marker)
	old, err := readIntent(root)
	if err != nil || old == nil {
		t.Fatal(old, err)
	}
	if other, err := NewWindowsRestrictedHost(root, os.Getenv("P1_WORKER")); err == nil {
		other.Close()
		t.Fatal("Второй владелец принят")
	}
	var handles []windows.Handle
	for _, pid := range pids[1:] {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { windows.CloseHandle(handle) })
		handles = append(handles, handle)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("Supervisor не завершён аварийно")
	}
	h, err := NewWindowsRestrictedHost(root, os.Getenv("P1_WORKER"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, handle := range handles {
		status, err := windows.WaitForSingleObject(handle, 2000)
		if err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatal("Старое дерево живо", status, err)
		}
	}
	previous := intentContainer(root, *old)
	if path, err := os.ReadFile(marker + ".profile"); err == nil {
		if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
			t.Fatal("Осталось хранилище профиля", err)
		}
	} else if stage != "intent" {
		t.Fatal(err)
	}
	if _, err := os.Stat(previous.directory); !os.IsNotExist(err) {
		t.Fatal("Остался старый снимок", err)
	}
	// Создание с прежним именем должно пройти: профиль действительно удалён.
	if err := previous.create(); err != nil {
		t.Fatal("Старый профиль остался", err)
	}
	result, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(previous.name)))
	if int32(result) < 0 {
		t.Fatal(result)
	}
	_ = windows.FreeSid(previous.sid)
	winHealthy(t, h)
	winEmpty(t, h)
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, containerJournal)); !os.IsNotExist(err) {
		t.Fatal("Журнал не удалён", err)
	}
	t.Logf("stage=%s old_workers=%d recovered=true healthy=true", stage, len(handles))
}

func containerFolder(t *testing.T, c *appContainer) string {
	t.Helper()
	sid, err := windows.UTF16PtrFromString(c.sid.String())
	if err != nil {
		t.Fatal(err)
	}
	var path *uint16
	result, _, _ := userenv.NewProc("GetAppContainerFolderPath").Call(uintptr(unsafe.Pointer(sid)), uintptr(unsafe.Pointer(&path)))
	if int32(result) < 0 {
		t.Fatal(result)
	}
	defer windows.NewLazySystemDLL("ole32.dll").NewProc("CoTaskMemFree").Call(uintptr(unsafe.Pointer(path)))
	folder := windows.UTF16PtrToString(path)
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	return folder
}

func profileSentinel(t *testing.T, c *appContainer) {
	t.Helper()
	folder := containerFolder(t, c)
	if err := os.WriteFile(filepath.Join(folder, "recovery-sentinel"), []byte("temporary profile data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("P1_LPAC_MARKER")+".profile", []byte(folder), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsRestrictedForeignJournal(t *testing.T) {
	h := restrictedHost(t, "P1_WORKER")
	raw, err := os.ReadFile(h.platform.container.journal)
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, containerJournal), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if candidate, err := NewWindowsRestrictedHost(other, os.Getenv("P1_WORKER")); err == nil {
		candidate.Close()
		t.Fatal("Чужой журнал принят")
	}
	winHealthy(t, h)
}

func TestWindowsRestrictedJournalLink(t *testing.T) {
	if os.Getenv("P1_WORKER") == "" {
		t.Skip("Требуется Windows CI")
	}
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "preserve")
	if err := os.WriteFile(target, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, containerJournal)); err != nil {
		t.Fatal(err)
	}
	if h, err := NewWindowsRestrictedHost(root, os.Getenv("P1_WORKER")); err == nil {
		h.Close()
		t.Fatal("Ссылка вместо журнала принята")
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "sentinel" {
		t.Fatal("Внешний файл изменён", err)
	}
}

func TestWindowsRestrictedCorruptJournal(t *testing.T) {
	if os.Getenv("P1_WORKER") == "" {
		t.Skip("Требуется Windows CI")
	}
	for _, raw := range []string{`{`, `{"version":2,"root":"x","id":"x"}`, `{"version":1,"root":"x","id":"../victim"}`} {
		root := t.TempDir()
		path := filepath.Join(root, containerJournal)
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if h, err := NewWindowsRestrictedHost(root, os.Getenv("P1_WORKER")); err == nil {
				h.Close()
				t.Fatal("Повреждённый журнал принят")
			}
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != raw {
			t.Fatal("Журнал изменён", err)
		}
	}
}

func TestWindowsRestrictedCleanupRetry(t *testing.T) {
	h := restrictedHost(t, "P1_WORKER")
	journal := h.platform.container.journal
	extra := filepath.Join(h.group, "unrecognized")
	if err := os.WriteFile(extra, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err == nil {
		t.Fatal("Посторонний файл удалён")
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(extra); err != nil || string(raw) != "preserve" {
		t.Fatal("Посторонние данные потеряны", err)
	}
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
