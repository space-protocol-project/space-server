//go:build windows

package extensions

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type resourceRequest struct {
	Mode, Registry, Event, Pipe string
	Process                     uint32
}
type resourceResult struct {
	Allowed bool   `json:"allowed"`
	Code    uint32 `json:"code"`
	Error   string `json:"error"`
}

func resourceResults(t *testing.T, h *ProcessHost, request resourceRequest) map[string]resourceResult {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(workerRequest{Request: raw})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := h.run(ctx, input)
	if err != nil {
		t.Fatalf("probe: %s %v", output, err)
	}
	var results map[string]resourceResult
	if err := json.Unmarshal(output, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 8 {
		t.Fatal("Неполный результат", string(output))
	}
	t.Log(string(output))
	winEmpty(t, h)
	return results
}

func privatePipe(t *testing.T, name string) windows.Handle {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	// Duplex, first instance, byte mode, одна ожидающая client-инстанция.
	handle, _, failure := windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateNamedPipeW").Call(uintptr(unsafe.Pointer(pointer)), 0x3|0x80000, 0, 1, 1024, 1024, 1000, 0)
	if windows.Handle(handle) == windows.InvalidHandle {
		t.Fatal(failure)
	}
	return windows.Handle(handle)
}

type resourceFixture struct {
	request     resourceRequest
	key         registry.Key
	path        string
	event, pipe windows.Handle
}

func newResourceFixture(t *testing.T) *resourceFixture {
	t.Helper()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	leaf := fmt.Sprintf("SpaceP1Rights-%x", nonce)
	f := &resourceFixture{path: `Software\` + leaf}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, f.path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	f.key = key
	t.Cleanup(func() { f.dispose(t) })
	if err := key.SetStringValue("sentinel", "secret"); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	f.event, f.request.Event = namedEvent(t)
	f.request.Mode = "resources"
	f.request.Registry = user.User.Sid.String() + `\` + f.path
	f.request.Pipe = `\\.\pipe\` + leaf
	f.request.Process = uint32(os.Getpid())
	f.pipe = privatePipe(t, f.request.Pipe)
	return f
}

func (f *resourceFixture) dispose(t *testing.T) {
	t.Helper()
	if f.pipe != 0 {
		windows.CloseHandle(f.pipe)
	}
	f.key.Close()
	_ = registry.DeleteKey(registry.CURRENT_USER, f.path+`\child`)
	if err := registry.DeleteKey(registry.CURRENT_USER, f.path); err != nil {
		t.Error(err)
	}
}

func (f *resourceFixture) reset(t *testing.T) {
	t.Helper()
	value, _, err := f.key.GetStringValue("sentinel")
	if err != nil || value != "tampered" {
		t.Fatal("Контроль не изменил реестр", err)
	}
	if err := f.key.SetStringValue("sentinel", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := registry.DeleteKey(registry.CURRENT_USER, f.path+`\child`); err != nil {
		t.Fatal(err)
	}
	if state, err := windows.WaitForSingleObject(f.event, 0); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatal("Контроль не изменил событие", state, err)
	}
	if err := windows.ResetEvent(f.event); err != nil {
		t.Fatal(err)
	}
	if err := windows.CloseHandle(f.pipe); err != nil {
		t.Fatal(err)
	}
	f.pipe = 0
	f.pipe = privatePipe(t, f.request.Pipe)
}

func (f *resourceFixture) intact(t *testing.T) {
	t.Helper()
	value, _, err := f.key.GetStringValue("sentinel")
	if err != nil || value != "secret" {
		t.Fatal("Реестр изменён", err)
	}
	child, err := registry.OpenKey(registry.CURRENT_USER, f.path+`\child`, registry.READ)
	if err == nil {
		child.Close()
		t.Fatal("Создан дочерний ключ")
	}
	if err != windows.ERROR_FILE_NOT_FOUND {
		t.Fatal(err)
	}
	if state, err := windows.WaitForSingleObject(f.event, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("Событие изменено", state, err)
	}
}

func assertResourceDenials(t *testing.T, results map[string]resourceResult) {
	t.Helper()
	for name, result := range results {
		if result.Allowed {
			t.Fatalf("LPAC получил доступ %s", name)
		}
		// FILE_NOT_FOUND допустим только для изолированного пространства имён события.
		if result.Code != uint32(windows.ERROR_ACCESS_DENIED) && !(name == "event" && result.Code == uint32(windows.ERROR_FILE_NOT_FOUND)) {
			t.Fatalf("Неожиданный отказ %s: %+v", name, result)
		}
	}
}

func TestWindowsRestrictedResources(t *testing.T) {
	h := restrictedHost(t, "P1_RIGHTS")
	control, err := NewProcessHost(t.TempDir(), os.Getenv("P1_RIGHTS"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	f := newResourceFixture(t)
	baseline := resourceResults(t, control, f.request)
	for name, result := range baseline {
		if !result.Allowed || result.Code != 0 {
			t.Fatalf("control %s: %+v", name, result)
		}
	}
	f.reset(t)
	assertResourceDenials(t, resourceResults(t, h, f.request))
	f.intact(t)
}
