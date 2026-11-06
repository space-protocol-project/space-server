//go:build windows

package extensions

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func requireClientRejected(t *testing.T, h *ProcessHost, done <-chan pipeWorkerResult) {
	t.Helper()
	result := awaitPipeClient(t, done)
	if result.err != nil {
		t.Fatal("Нет ответа проверки сервера", result.err)
	}
	var response resourceResult
	if err := json.Unmarshal(result.output, &response); err != nil {
		t.Fatal(err)
	}
	if response.Allowed || response.Code != uint32(windows.ERROR_ACCESS_DENIED) {
		t.Fatal(string(result.output))
	}
	winEmpty(t, h)
}

func requireNoRequest(t *testing.T, pipe windows.Handle) {
	t.Helper()
	var data [4]byte
	count, err := profilePipeIO(t, pipe, data[:], false)
	if count != 0 || !errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		t.Fatal("Сервер получил данные до проверки", count, err)
	}
}

func rejectedServerProof(t *testing.T, wrongSession bool) {
	h := restrictedHost(t, "P1_RIGHTS")
	f := newProfileIPC(t, h)
	launch := prepareProfileClient(t, h)
	if wrongSession {
		launch.sessionOffset = 1
	}
	pipe := f.openServerFlags(t, windows.FILE_FLAG_OVERLAPPED)
	defer windows.CloseHandle(pipe)
	cancel, done := launch.start(t, f.pipeLocal)
	defer cancel()
	if err := connectProfilePipe(t, pipe); err != nil {
		t.Fatal(err)
	}
	if !wrongSession {
		nonce := launch.nonce
		nonce[0] ^= 1
		if count, err := profilePipeIO(t, pipe, nonce[:], true); err != nil || count != 16 {
			t.Fatal(count, err)
		}
	}
	requireClientRejected(t, h, done)
	requireNoRequest(t, pipe)
	t.Logf("wrong_session=%v server_rejected=true request_bytes=0", wrongSession)
}

func TestWindowsRestrictedServerProof(t *testing.T) {
	t.Run("wrong-nonce", func(t *testing.T) { rejectedServerProof(t, false) })
	t.Run("wrong-session", func(t *testing.T) { rejectedServerProof(t, true) })
}

// Отдельный обычный процесс намеренно занимает разрешённый LPAC endpoint.
func TestWindowsRestrictedPipeSpoofHelper(t *testing.T) {
	marker := os.Getenv("P1_SPOOF_MARKER")
	if marker == "" {
		t.Skip("Вспомогательный процесс подставного сервера")
	}
	sid, err := windows.StringToSid(os.Getenv("P1_SPOOF_SID"))
	if err != nil {
		t.Fatal(err)
	}
	f := &profileIPC{pipeName: os.Getenv("P1_SPOOF_PIPE"), pipeSD: profileObjectSD(t, sid, windows.FILE_READ_DATA|windows.FILE_WRITE_DATA|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE)}
	pipe := f.openServerFlags(t, windows.FILE_FLAG_OVERLAPPED)
	defer windows.CloseHandle(pipe)
	raw, _ := json.Marshal([]uint32{uint32(os.Getpid())})
	if err := os.WriteFile(marker, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := connectProfilePipe(t, pipe); err != nil {
		t.Fatal(err)
	}
	requireNoRequest(t, pipe)
	if err := os.WriteFile(marker+".result", []byte("request_bytes=0"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsRestrictedPipeSpoof(t *testing.T) {
	h := restrictedHost(t, "P1_RIGHTS")
	f := newProfileIPC(t, h)
	launch := prepareProfileClient(t, h)
	marker := filepath.Join(t.TempDir(), "ready.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsRestrictedPipeSpoofHelper$", "-test.timeout=15s")
	cmd.Env = append(os.Environ(), "P1_SPOOF_MARKER="+marker, "P1_SPOOF_SID="+f.sid, "P1_SPOOF_PIPE="+f.pipeName)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pids := waitDeathMarker(t, marker)
	if len(pids) != 1 || pids[0] != uint32(cmd.Process.Pid) || pids[0] == uint32(os.Getpid()) {
		t.Fatal("Неверный PID helper", pids)
	}
	assertEndpointClaimed(t, f)
	cancel, done := launch.start(t, f.pipeLocal)
	defer cancel()
	requireClientRejected(t, h, done)
	if err := cmd.Wait(); err != nil {
		t.Fatal("Подставной сервер", err)
	}
	result, err := os.ReadFile(marker + ".result")
	if err != nil || string(result) != "request_bytes=0" {
		t.Fatal("Нет подтверждения отсутствия запроса", err)
	}
	t.Logf("server_pid=%d expected_pid=%d rejected=true request_bytes=0 first_instance_denied=true", pids[0], os.Getpid())
}

func assertEndpointClaimed(t *testing.T, f *profileIPC) {
	t.Helper()
	pipe, err := f.createServer(windows.FILE_FLAG_OVERLAPPED)
	if err == nil {
		windows.CloseHandle(pipe)
		t.Fatal("Занятое имя принято")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatal("Неожиданный отказ занятому имени", err)
	}
}
