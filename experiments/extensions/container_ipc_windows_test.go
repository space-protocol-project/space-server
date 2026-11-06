//go:build windows

package extensions

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

type profileIPC struct {
	eventName, pipeName string
	pipeLocal, sid      string
	event               windows.Handle
	pipeSD              *windows.SECURITY_DESCRIPTOR
}

func profileObjectPath(t *testing.T, sid *windows.SID) string {
	t.Helper()
	var buffer [1024]uint16
	var size uint32
	ok, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetAppContainerNamedObjectPath").Call(0, uintptr(unsafe.Pointer(sid)), uintptr(len(buffer)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		t.Fatal("GetAppContainerNamedObjectPath", err)
	}
	var session uint32
	ok, _, err = windows.NewLazySystemDLL("kernel32.dll").NewProc("ProcessIdToSessionId").Call(uintptr(os.Getpid()), uintptr(unsafe.Pointer(&session)))
	if ok == 0 {
		t.Fatal("ProcessIdToSessionId", err)
	}
	return fmt.Sprintf(`Sessions\%d\%s`, session, windows.UTF16ToString(buffer[:]))
}

func profileObjectSD(t *testing.T, sid *windows.SID, mask uint32) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// Low integrity допускает запись LPAC, но DACL ограничивает её одним SID.
	sddl := fmt.Sprintf("D:P(A;;GA;;;%s)(A;;GA;;;SY)(A;;0x%x;;;%s)S:(ML;;NW;;;LW)", user.User.Sid.String(), mask, sid.String())
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	return sd
}

func newProfileIPC(t *testing.T, h *ProcessHost) *profileIPC {
	t.Helper()
	name := "SpaceP1IPC-" + rand.Text()
	root := profileObjectPath(t, h.platform.container.sid)
	f := &profileIPC{eventName: `Global\` + name, pipeName: `\\.\pipe\` + strings.TrimPrefix(root, `\`) + `\` + name, pipeLocal: `\\.\pipe\LOCAL\` + name, sid: h.platform.container.sid.String()}
	t.Logf("container_objects=%q pipe=%q", root, f.pipeName)
	sd := profileObjectSD(t, h.platform.container.sid, windows.EVENT_MODIFY_STATE|windows.SYNCHRONIZE)
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	pointer, err := windows.UTF16PtrFromString(f.eventName)
	if err != nil {
		t.Fatal(err)
	}
	f.event, err = windows.CreateEvent(&attributes, 1, 0, pointer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(f.event) })
	f.pipeSD = profileObjectSD(t, h.platform.container.sid, windows.FILE_READ_DATA|windows.FILE_WRITE_DATA|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE)
	return f
}

func (f *profileIPC) openServer(t *testing.T) windows.Handle {
	return f.openServerFlags(t, 0)
}

func (f *profileIPC) openServerFlags(t *testing.T, flags uint32) windows.Handle {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(f.pipeName)
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: f.pipeSD}
	// Duplex, первая и единственная инстанция, локальные клиенты, byte mode.
	handle, _, failure := windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateNamedPipeW").Call(uintptr(unsafe.Pointer(pointer)), uintptr(0x3|0x80000|flags), 0x8, 1, 1024, 1024, 1000, uintptr(unsafe.Pointer(&attributes)))
	if windows.Handle(handle) == windows.InvalidHandle {
		t.Fatal(failure)
	}
	return windows.Handle(handle)
}

func checkProfileIPC(t *testing.T, h *ProcessHost, f *profileIPC, allowed bool) {
	t.Helper()
	if err := windows.ResetEvent(f.event); err != nil {
		t.Fatal(err)
	}
	pipe := f.openServer(t)
	defer windows.CloseHandle(pipe)
	var results map[string]resourceResult
	pipeName := f.pipeName
	if h.platform.container != nil && h.platform.container.sid.String() == f.sid {
		pipeName = f.pipeLocal
	}
	boundaryRun(t, h, map[string]string{"Mode": "profile-ipc", "Event": f.eventName, "Pipe": pipeName}, &results)
	if len(results) != 2 {
		t.Fatal("Неполный результат", results)
	}
	wantCode := uint32(windows.ERROR_ACCESS_DENIED)
	wantState := uint32(windows.WAIT_TIMEOUT)
	if allowed {
		wantCode, wantState = 0, windows.WAIT_OBJECT_0
	}
	for _, name := range []string{"event", "pipe"} {
		result, exists := results[name]
		// Чужой контейнер может скрывать имя pipe вместо отказа DACL.
		// Это отдельное наблюдение; своим клиентам по-прежнему нужен успех.
		if !allowed && name == "pipe" && exists && !result.Allowed && result.Code == uint32(windows.ERROR_FILE_NOT_FOUND) {
			t.Log("pipe_namespace_hidden=true dacl_denial_proven=false")
			continue
		}
		if !exists || result.Allowed != allowed || result.Code != wantCode {
			t.Fatalf("%s allowed=%v result=%+v", name, allowed, result)
		}
	}
	state, err := windows.WaitForSingleObject(f.event, 0)
	if err != nil || state != wantState {
		t.Fatal("Неверное состояние события", state, err)
	}
}

func TestWindowsRestrictedProfileIPC(t *testing.T) {
	a, b := restrictedHost(t, "P1_RIGHTS"), restrictedHost(t, "P1_RIGHTS")
	first, second := newProfileIPC(t, a), newProfileIPC(t, b)
	control := windowsHost(t, "P1_RIGHTS")
	t.Run("control-a", func(t *testing.T) { checkProfileIPC(t, control, first, true) })
	t.Run("control-b", func(t *testing.T) { checkProfileIPC(t, control, second, true) })
	t.Run("own-a", func(t *testing.T) { checkProfileIPC(t, a, first, true) })
	t.Run("own-b", func(t *testing.T) { checkProfileIPC(t, b, second, true) })
	t.Run("a-to-b", func(t *testing.T) { checkProfileIPC(t, a, second, false) })
	t.Run("b-to-a", func(t *testing.T) { checkProfileIPC(t, b, first, false) })
	t.Run("own-a-after-cross", func(t *testing.T) { checkProfileIPC(t, a, first, true) })
	t.Run("own-b-after-cross", func(t *testing.T) { checkProfileIPC(t, b, second, true) })
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	t.Run("surviving-b", func(t *testing.T) { checkProfileIPC(t, b, second, true) })
	c := restrictedHost(t, "P1_RIGHTS")
	t.Run("replacement-to-old-a", func(t *testing.T) { checkProfileIPC(t, c, first, false) })
	t.Run("replacement-to-b", func(t *testing.T) { checkProfileIPC(t, c, second, false) })
}
