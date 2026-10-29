//go:build linux

package extensions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type processPlatform struct{ lock *os.File }

func configureGroup(path string, memory uint64) error {
	if _, err := os.Stat(filepath.Join(path, "cgroup.kill")); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"memory.max": strconv.FormatUint(memory, 10), "memory.swap.max": "0",
		"memory.oom.group": "1", "pids.max": "64", "cpu.max": "100000 100000",
	} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0600); err != nil {
			return err
		}
	}
	return nil
}

// NewProcessHost требует выделенный root-owned cgroup v2 с доступными контроллерами.
// Прототип запускает worker под UID/GID 65534 без дополнительных групп.
func NewProcessHost(root, executable string) (*ProcessHost, error) {
	if os.Geteuid() != 0 || !filepath.IsAbs(root) || !filepath.IsAbs(executable) {
		return nil, ErrIsolation
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(root, &fs); err != nil || fs.Type != 0x63677270 {
		return nil, ErrIsolation
	}
	lock, err := lockRoot(root)
	if err != nil {
		return nil, fmt.Errorf("%w: ownership: %v", ErrIsolation, err)
	}
	h, err := newLinuxHost(root, executable)
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("%w: recovery: %v", ErrIsolation, err)
	}
	h.platform.lock = lock
	return h, nil
}

func newLinuxHost(root, executable string) (*ProcessHost, error) {
	if err := recoverGroups(root); err != nil {
		return nil, err
	}
	group, err := os.MkdirTemp(root, "space-p1-")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIsolation, err)
	}
	if err = configureGroup(group, TotalMemory); err == nil {
		err = os.WriteFile(filepath.Join(group, "cgroup.subtree_control"), []byte("+memory +pids +cpu"), 0600)
	}
	if err != nil {
		_ = os.Remove(group)
		return nil, fmt.Errorf("%w: %v", ErrIsolation, err)
	}
	return &ProcessHost{executable: executable, group: group, ready: true}, nil
}

func (h *ProcessHost) run(ctx context.Context, input []byte) (result []byte, failure error) {
	group, err := os.MkdirTemp(h.group, "call-")
	if err != nil {
		return nil, ErrIsolation
	}
	defer func() {
		if err := removeGroup(group); err != nil {
			h.mu.Lock()
			h.closed = true
			h.mu.Unlock()
			result, failure = nil, fmt.Errorf("%w: cleanup: %v", ErrIsolation, err)
		}
	}()
	if err = configureGroup(group, WorkerMemory); err != nil {
		return nil, ErrIsolation
	}
	fd, err := os.Open(group)
	if err != nil {
		return nil, ErrIsolation
	}
	defer fd.Close()
	cmd := exec.CommandContext(ctx, h.executable)
	cmd.Dir = "/"
	cmd.Env = []string{"GOMAXPROCS=2", "GOMEMLIMIT=64MiB"}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		UseCgroupFD: true, CgroupFD: int(fd.Fd()), Pdeathsig: syscall.SIGKILL,
		Credential: &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{}},
	}
	cmd.Stdin = bytes.NewReader(input)
	stdout := &cappedOutput{limit: MaxJSON}
	stderr := &cappedOutput{limit: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Cancel = func() error { return killGroup(group) }
	cmd.WaitDelay = 250 * time.Millisecond
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, ErrQuota
	}
	if err != nil {
		return nil, ErrWorker
	}
	return stdout.data, nil
}

func killGroup(group string) error {
	return os.WriteFile(filepath.Join(group, "cgroup.kill"), []byte("1"), 0600)
}

func removeGroup(group string) error {
	if err := killGroup(group); err != nil {
		return err
	}
	deadline := time.Now().Add(time.Second)
	for {
		err := os.Remove(group)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readCounter(group, name string) uint64 {
	raw, _ := os.ReadFile(filepath.Join(group, name))
	value, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return value
}

func (h *ProcessHost) Stats() ProcessStats {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := ProcessStats{Active: h.active, ReservedBytes: uint64(h.active) * WorkerMemory,
		CurrentBytes: readCounter(h.group, "memory.current"), PeakBytes: readCounter(h.group, "memory.peak")}
	raw, _ := os.ReadFile(filepath.Join(h.group, "memory.events"))
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "oom_kill" {
			result.OOMKills, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	raw, _ = os.ReadFile(filepath.Join(h.group, "cpu.stat"))
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[0] == "usage_usec" {
			result.CPUUsec, _ = strconv.ParseUint(fields[1], 10, 64)
		}
		if fields[0] == "nr_throttled" {
			result.Throttled, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return result
}

// Close запрещает новые вызовы. Занятый хост закрывается повторно после их завершения.
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
	if err := os.Remove(h.group); err != nil {
		return err
	}
	h.ready = false
	return h.platform.lock.Close()
}
