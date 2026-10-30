//go:build windows

package extensions

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows SDK: ProcThreadAttributeValue(13, FALSE, TRUE, FALSE).
const procThreadAttributeJobList = 0x0002000d

// Возвращает приостановленный процесс, уже принадлежащий обоим Job.
// Старого запуска с последующим AssignProcessToJobObject здесь нет.
func createAssigned(executable, directory string, jobs [2]windows.Handle, handles [3]windows.Handle) (windows.ProcessInformation, error) {
	var process windows.ProcessInformation
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return process, err
	}
	defer attributes.Delete()
	if err := attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(jobs)); err != nil {
		return process, err
	}
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
		return process, err
	}
	app, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return process, err
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{executable}))
	if err != nil {
		return process, err
	}
	dir, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return process, err
	}
	environment := utf16.Encode([]rune(strings.Join([]string{"GOMEMLIMIT=64MiB", "GOMAXPROCS=2", "SystemRoot=" + os.Getenv("SystemRoot"), "", ""}, "\x00")))
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput, startup.StdOutput, startup.StdErr = handles[0], handles[1], handles[2]
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	err = windows.CreateProcess(app, command, nil, nil, true, flags, &environment[0], dir, &startup.StartupInfo, &process)
	return process, err
}

func (h *ProcessHost) runAssigned(ctx context.Context, input []byte, job windows.Handle) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pipes, err := newWorkerPipes()
	if err != nil {
		return nil, ErrIsolation
	}
	defer pipes.close()
	process, err := createAssigned(h.executable, h.group, [2]windows.Handle{h.platform.job, job}, pipes.child)
	pipes.closeChild()
	if err != nil {
		return nil, fmt.Errorf("%w: create assigned: %v", ErrIsolation, err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	previous, err := windows.ResumeThread(process.Thread)
	if err != nil || previous != 1 {
		_ = windows.TerminateJobObject(job, 1)
		return nil, ErrIsolation
	}
	return exchangeProcess(ctx, process.Process, job, pipes, input)
}

func waitProcess(ctx context.Context, process windows.Handle) error {
	for {
		status, err := windows.WaitForSingleObject(process, 10)
		if err != nil {
			return ErrIsolation
		}
		if status == windows.WAIT_OBJECT_0 {
			var code uint32
			if windows.GetExitCodeProcess(process, &code) != nil || code != 0 {
				return ErrWorker
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func exchangeProcess(ctx context.Context, process, job windows.Handle, pipes *workerPipes, input []byte) ([]byte, error) {
	stdout, stderr := &cappedOutput{limit: MaxJSON}, &cappedOutput{limit: 4096}
	stop := func() { _ = windows.TerminateJobObject(job, 1) }
	stdout.onOverflow, stderr.onOverflow = stop, stop
	done := pipes.exchange(input, stdout, stderr)
	err := waitProcess(ctx, process)
	// Потомки могут удерживать stdout после завершения главного процесса.
	if failure := stopJob(job); failure != nil {
		return nil, ErrIsolation
	}
	pipeError := drainPipes(done)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, ErrQuota
	}
	if pipeError != nil {
		return nil, pipeError
	}
	return stdout.data, err
}

func drainPipes(done <-chan error) error {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	var failure error
	for i := 0; i < 3; i++ {
		select {
		case err := <-done:
			if err != nil {
				failure = ErrWorker
			}
		case <-timer.C:
			return ErrIsolation
		}
	}
	return failure
}
