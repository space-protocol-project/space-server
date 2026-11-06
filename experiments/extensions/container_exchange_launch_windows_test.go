//go:build windows

package extensions

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type profileClientLaunch struct {
	job           windows.Handle
	pipes         *workerPipes
	process       windows.ProcessInformation
	cancel        context.CancelFunc
	finished      chan struct{}
	nonce         [16]byte
	sessionOffset uint32
}

// Пространство нового LPAC-процесса должно существовать до создания pipe.
// Процесс уже в обоих Job, но не исполняется до готовности сервера.
func prepareProfileClient(t *testing.T, h *ProcessHost) *profileClientLaunch {
	t.Helper()
	l := &profileClientLaunch{}
	if _, err := rand.Read(l.nonce[:]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.close(t) })
	var err error
	l.job, err = createJob(WorkerMemory)
	if err != nil {
		t.Fatal(err)
	}
	l.pipes, err = newWorkerPipes()
	if err != nil {
		t.Fatal(err)
	}
	l.process, err = createContained(h.executable, h.group, [2]windows.Handle{h.platform.job, l.job}, l.pipes.child, h.platform.container)
	l.pipes.closeChild()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.platform.container.verify(l.process.Process); err != nil {
		t.Fatal(err)
	}
	return l
}

func (l *profileClientLaunch) start(t *testing.T, name string) (context.CancelFunc, <-chan pipeWorkerResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	l.cancel = cancel
	var session uint32
	ok, _, failure := windows.NewLazySystemDLL("kernel32.dll").NewProc("ProcessIdToSessionId").Call(uintptr(os.Getpid()), uintptr(unsafe.Pointer(&session)))
	if ok == 0 {
		t.Fatal(failure)
	}
	session += l.sessionOffset
	input := boundaryInput(t, map[string]any{"Mode": "pipe-exchange", "Pipe": name, "ServerPID": uint32(os.Getpid()), "ServerSession": session, "Nonce": l.nonce[:]})
	previous, err := windows.ResumeThread(l.process.Thread)
	if err != nil || previous != 1 {
		t.Fatal("ResumeThread", previous, err)
	}
	done := make(chan pipeWorkerResult, 1)
	l.finished = make(chan struct{})
	go func() {
		defer close(l.finished)
		output, err := exchangeProcess(ctx, l.process.Process, l.job, l.pipes, input)
		done <- pipeWorkerResult{output, err}
	}()
	return cancel, done
}

func (l *profileClientLaunch) close(t *testing.T) {
	t.Helper()
	if l.cancel != nil {
		l.cancel()
	}
	if l.job != 0 {
		if err := stopJob(l.job); err != nil {
			t.Error(err)
		}
	}
	if l.finished != nil {
		select {
		case <-l.finished:
		case <-time.After(5 * time.Second):
			t.Error("Обмен не завершился после остановки Job")
			return
		}
	}
	if l.pipes != nil {
		l.pipes.close()
	}
	for _, handle := range []windows.Handle{l.process.Thread, l.process.Process, l.job} {
		if handle != 0 {
			windows.CloseHandle(handle)
		}
	}
}
