//go:build windows

package extensions

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processPlatform struct{ job windows.Handle }

type jobAccounting struct {
	UserTime, KernelTime, PeriodUser, PeriodKernel               int64
	Faults, TotalProcesses, ActiveProcesses, TerminatedProcesses uint32
}

// Root для Windows — рабочий каталог доверенной оболочки, не cgroup.
func NewProcessHost(root, executable string) (*ProcessHost, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(executable) {
		return nil, ErrIsolation
	}
	directory, err := os.Stat(root)
	if err != nil || !directory.IsDir() {
		return nil, ErrIsolation
	}
	binary, err := os.Stat(executable)
	if err != nil || !binary.Mode().IsRegular() {
		return nil, ErrIsolation
	}
	job, err := createJob(TotalMemory)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIsolation, err)
	}
	// Общая квота CPU — 50% доступного Windows процессорного времени.
	cpu := struct{ Flags, Rate uint32 }{1 | 4, 5000}
	_, err = windows.SetInformationJobObject(job, windows.JobObjectCpuRateControlInformation, uintptr(unsafe.Pointer(&cpu)), uint32(unsafe.Sizeof(cpu)))
	if err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("%w: CPU: %v", ErrIsolation, err)
	}
	return &ProcessHost{executable: executable, group: root, ready: true, platform: processPlatform{job: job}}, nil
}

func createJob(memory uintptr) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	if err = setJobLimit(job, memory); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func setJobLimit(job windows.Handle, memory uintptr) error {
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_JOB_MEMORY | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS | windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	info.BasicLimitInformation.ActiveProcessLimit = 64
	info.JobMemoryLimit = memory
	_, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return err
}

func (h *ProcessHost) run(ctx context.Context, input []byte) (result []byte, failure error) {
	job, err := createJob(WorkerMemory)
	if err != nil {
		return nil, ErrIsolation
	}
	defer windows.CloseHandle(job)
	defer func() {
		if err := stopJob(job); err != nil {
			h.mu.Lock()
			h.closed = true
			h.mu.Unlock()
			result, failure = nil, fmt.Errorf("%w: cleanup: %v", ErrIsolation, err)
		}
	}()
	cmd := exec.CommandContext(ctx, h.executable)
	cmd.Dir = h.group
	cmd.Env = []string{"GOMAXPROCS=2", "GOMEMLIMIT=64MiB"}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED}
	cmd.Stdin = bytes.NewReader(input)
	stdout, stderr := &cappedOutput{limit: MaxJSON}, &cappedOutput{limit: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Cancel = func() error {
		// Отмена возможна и до назначения приостановленного процесса в Job.
		_ = windows.TerminateJobObject(job, 1)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 250 * time.Millisecond
	err = startAssigned(cmd, h.platform.job, job)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, ErrQuota
	}
	if err != nil {
		return nil, err
	}
	return stdout.data, nil
}

func startAssigned(cmd *exec.Cmd, parent, child windows.Handle) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: start: %v", ErrIsolation, err)
	}
	if err := assignAndResume(uint32(cmd.Process.Pid), parent, child); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("%w: assign: %v", ErrIsolation, err)
	}
	if err := cmd.Wait(); err != nil {
		return ErrWorker
	}
	return nil
}

func assignAndResume(pid uint32, parent, child windows.Handle) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	if err = windows.AssignProcessToJobObject(parent, process); err != nil {
		return err
	}
	if err = windows.AssignProcessToJobObject(child, process); err != nil {
		return err
	}
	return resumePrimary(pid)
}

// CREATE_SUSPENDED не позволяет primary thread выполнить код до назначения Job.
func resumePrimary(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID == pid {
			return resumeThread(entry.ThreadID)
		}
	}
	return err
}

func resumeThread(id uint32) error {
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, id)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(thread)
	previous, err := windows.ResumeThread(thread)
	if err == nil && previous != 1 {
		return ErrIsolation
	}
	return err
}

func accounting(job windows.Handle) (jobAccounting, error) {
	var info jobAccounting
	err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	return info, err
}

func stopJob(job windows.Handle) error {
	if err := windows.TerminateJobObject(job, 1); err != nil {
		return err
	}
	deadline := time.Now().Add(time.Second)
	for {
		info, err := accounting(job)
		if err != nil {
			return err
		}
		if info.ActiveProcesses == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrIsolation
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *ProcessHost) Stats() ProcessStats {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := ProcessStats{Active: h.active, ReservedBytes: uint64(h.active) * WorkerMemory}
	var memory windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if windows.QueryInformationJobObject(h.platform.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&memory)), uint32(unsafe.Sizeof(memory)), nil) == nil {
		result.PeakBytes = uint64(memory.PeakJobMemoryUsed)
	}
	if info, err := accounting(h.platform.job); err == nil {
		result.CPUUsec = uint64(info.UserTime+info.KernelTime) / 10
		result.OSProcesses = info.ActiveProcesses
	}
	return result
}

func (h *ProcessHost) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.active != 0 {
		return ErrQuota
	}
	if !h.ready {
		return nil
	}
	if err := stopJob(h.platform.job); err != nil {
		return err
	}
	err := windows.CloseHandle(h.platform.job)
	if err == nil {
		h.ready = false
		h.platform.job = 0
	}
	return err
}
