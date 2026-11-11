//go:build windows

package extensions

import (
	"golang.org/x/sys/windows"
	"testing"
	"unsafe"
)

// Только диагностический стенд: конструктор хоста не выдаёт capabilities.
func diagnosticCapability(t *testing.T, name string) windows.SIDAndAttributes {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	var groups, capabilities **windows.SID
	var groupCount, count uint32
	result, _, _ := windows.NewLazySystemDLL("kernelbase.dll").NewProc("DeriveCapabilitySidsFromName").Call(
		uintptr(unsafe.Pointer(pointer)), uintptr(unsafe.Pointer(&groups)), uintptr(unsafe.Pointer(&groupCount)),
		uintptr(unsafe.Pointer(&capabilities)), uintptr(unsafe.Pointer(&count)))
	if int32(result) < 0 {
		t.Fatalf("DeriveCapabilitySidsFromName: %#x", result)
	}
	defer freeDiagnosticSIDs(groups, groupCount)
	defer freeDiagnosticSIDs(capabilities, count)
	if count != 1 {
		t.Fatalf("capability count: %d", count)
	}
	sid, err := windows.StringToSid((*capabilities).String())
	if err != nil {
		t.Fatal(err)
	}
	return windows.SIDAndAttributes{Sid: sid, Attributes: windows.SE_GROUP_ENABLED}
}

func freeDiagnosticSIDs(values **windows.SID, count uint32) {
	for _, sid := range unsafe.Slice(values, count) {
		_, _ = windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(sid))))
	}
	_, _ = windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(values))))
}

func TestWindowsRestrictedWinsockReady(t *testing.T) {
	h := restrictedHost(t, "P1_RIGHTS")
	h.platform.container.capabilities = []windows.SIDAndAttributes{diagnosticCapability(t, "registryRead")}
	for _, network := range []string{"tcp4", "tcp6", "udp4", "udp6"} {
		t.Run(network, func(t *testing.T) {
			results := networkResults(t, h, network, echoEndpoint(t, network))
			if !results["startup"].Allowed {
				t.Fatalf("Winsock must initialize: %+v", results)
			}
			assertNetworkBoundary(t, results)
		})
	}
}
