//go:build linux

package extensions

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Service проверяет жизненный цикл отдельной службы; прикладного API пока нет.
func Service(ctx context.Context, executable string) error {
	h, err := serviceHost(executable)
	if err != nil {
		return err
	}
	if err := serviceReady(); err != nil {
		return errors.Join(err, h.Close())
	}
	<-ctx.Done()
	return h.Close()
}

func serviceHost(executable string) (*ProcessHost, error) {
	binary, err := os.Stat(executable)
	if err != nil || !filepath.IsAbs(executable) || !binary.Mode().IsRegular() {
		return nil, ErrIsolation
	}
	root, err := serviceRoot()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("+memory +pids +cpu"), 0600); err != nil {
		return nil, err
	}
	workers := filepath.Join(root, "workers")
	if err := os.Mkdir(workers, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(workers, "cgroup.subtree_control"), []byte("+memory +pids +cpu"), 0600); err != nil {
		return nil, err
	}
	return NewProcessHost(workers, executable)
}

// Требует DelegateSubgroup=supervisor; не перемещает процесс из чужой cgroup.
func serviceRoot() (string, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "0::/") {
			continue
		}
		path := filepath.Clean(strings.TrimPrefix(line, "0::"))
		if filepath.Base(path) != "supervisor" || !strings.HasSuffix(filepath.Dir(path), ".service") {
			return "", ErrIsolation
		}
		return filepath.Join("/sys/fs/cgroup", filepath.Dir(path)), nil
	}
	return "", ErrIsolation
}

func serviceReady() error {
	address := os.Getenv("NOTIFY_SOCKET")
	if address == "" {
		return ErrIsolation
	}
	connection, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: address, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer connection.Close()
	_, err = connection.Write([]byte("READY=1\nSTATUS=ProcessHost ready"))
	return err
}
