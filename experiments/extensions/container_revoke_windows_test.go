//go:build windows

package extensions

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"golang.org/x/sys/windows"
)

func acceptRevocationPipe(t *testing.T, h *ProcessHost, pipe windows.Handle, launch *profileClientLaunch) {
	t.Helper()
	if err := connectProfilePipe(t, pipe); err != nil {
		t.Fatal(err)
	}
	pid, err := verifyPipeClient(h, pipe)
	if err != nil || pid != launch.process.ProcessId {
		t.Fatal("Неверный процесс канала", pid, err)
	}
	if count, err := profilePipeIO(t, pipe, launch.nonce[:], true); err != nil || count != 16 {
		t.Fatal("Не передано подтверждение", count, err)
	}
	receiveRevocationPing(t, pipe)
}

func receiveRevocationPing(t *testing.T, pipe windows.Handle) {
	t.Helper()
	var buffer [4]byte
	if count, err := profilePipeIO(t, pipe, buffer[:], false); err != nil || count != 4 || string(buffer[:]) != "ping" {
		t.Fatal("Неверный запрос", count, err)
	}
}

func sendRevocationPong(t *testing.T, pipe windows.Handle) {
	t.Helper()
	if count, err := profilePipeIO(t, pipe, []byte("pong"), true); err != nil || count != 4 {
		t.Fatal("Не передан ответ", count, err)
	}
}

func requireRevocationResult(t *testing.T, done <-chan pipeWorkerResult, pid uint32, pending bool) {
	t.Helper()
	result := awaitPipeClient(t, done)
	if result.err != nil {
		t.Fatal(result.err, string(result.output))
	}
	var response struct {
		Read, Write, Reopen resourceResult
		ReadBytes           uint32
		PID                 uint32
		Pending             bool
	}
	if err := json.Unmarshal(result.output, &response); err != nil {
		t.Fatal(err)
	}
	if response.PID != pid || response.ReadBytes != 0 || response.Pending != pending {
		t.Fatal("Изменился процесс или прочитаны отозванные данные", string(result.output))
	}
	for name, access := range map[string]resourceResult{"read": response.Read, "write": response.Write} {
		if access.Allowed || (access.Code != uint32(windows.ERROR_PIPE_NOT_CONNECTED) && access.Code != uint32(windows.ERROR_BROKEN_PIPE)) {
			t.Fatal("Старый handle не отозван", name, string(result.output))
		}
	}
	if response.Reopen.Allowed || response.Reopen.Code != uint32(windows.ERROR_PIPE_BUSY) {
		t.Fatal("Отозванный endpoint доступен", string(result.output))
	}
	t.Logf("same_pid=%d read_bytes=0 old_handle_read=%d old_handle_write=%d reopen=%d control_survived=true pending_read=%v", pid, response.Read.Code, response.Write.Code, response.Reopen.Code, response.Pending)
}

func revokeProfilePipe(t *testing.T, buffered, pending bool) {
	h := restrictedHost(t, "P1_RIGHTS")
	a, b := newProfileIPC(t, h), newProfileIPC(t, h)
	launch := prepareProfileClient(t, h)
	data := a.openServerFlags(t, windows.FILE_FLAG_OVERLAPPED)
	defer windows.CloseHandle(data)
	control := b.openServerFlags(t, windows.FILE_FLAG_OVERLAPPED)
	defer windows.CloseHandle(control)
	cancel, done := launch.startRequest(t, map[string]any{"Mode": "pipe-revoke", "Pipe": a.pipeLocal, "Control": b.pipeLocal, "Async": pending})
	defer cancel()
	acceptRevocationPipe(t, h, data, launch)
	sendRevocationPong(t, data)
	acceptRevocationPipe(t, h, control, launch)
	grant := newDeliveryGrant(pipeDeliveryWriter(t, data), func() error { return windows.DisconnectNamedPipe(data) })
	prepared, err := grant.prepare([]byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	if buffered {
		if err := grant.deliver(prepared); err != nil {
			t.Fatal("Не подготовлены непрочитанные данные", err)
		}
	}
	// Не меняем DACL и не завершаем Job: разрывается только этот экземпляр.
	if err := grant.revoke(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(grant.deliver(prepared), errDeliveryRevoked) {
		t.Fatal("Подготовленный ответ отправлен после отзыва")
	}
	sendRevocationPong(t, control)
	receiveRevocationPing(t, control)
	if state, err := windows.WaitForSingleObject(launch.process.Process, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("Worker завершён при отзыве", state, err)
	}
	if pid, err := verifyPipeClient(h, control); err != nil || pid != launch.process.ProcessId {
		t.Fatal("Другой канал потерял клиент", pid, err)
	}
	// Новое поколение использует другой, уже проверенный канал того же worker.
	if err := grant.renew(pipeDeliveryWriter(t, control), func() error { return windows.DisconnectNamedPipe(control) }); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(grant.deliver(prepared), errDeliveryRevoked) {
		t.Fatal("Старый ответ отправлен по новому каналу")
	}
	fresh, err := grant.prepare([]byte("pong"))
	if err != nil {
		t.Fatal(err)
	}
	if err := grant.deliver(fresh); err != nil {
		t.Fatal(err)
	}
	requireRevocationResult(t, done, launch.process.ProcessId, pending)
	winEmpty(t, h)
	t.Logf("buffered_response_discarded=%v job_termination_used=false stale_generation_denied=true renewed_delivery=true", buffered)
}

func pipeDeliveryWriter(t *testing.T, pipe windows.Handle) func([]byte) error {
	t.Helper()
	return func(body []byte) error {
		count, err := profilePipeIO(t, pipe, body, true)
		if err != nil {
			return err
		}
		if count != uint32(len(body)) {
			return fmt.Errorf("Неполный ответ: %d/%d", count, len(body))
		}
		return nil
	}
}

func TestWindowsRestrictedPipeRevocation(t *testing.T) {
	t.Run("idle", func(t *testing.T) { revokeProfilePipe(t, false, false) })
	t.Run("buffered-response", func(t *testing.T) { revokeProfilePipe(t, true, false) })
	t.Run("pending-read", func(t *testing.T) { revokeProfilePipe(t, false, true) })
}
