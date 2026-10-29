//go:build linux

package extensions

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Выделенный root и имена space-p1-* принадлежат только этому launcher.
// Дескриптор os.Open закрывается при exec и не передаётся worker.
func lockRoot(root string) (*os.File, error) {
	file, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		_ = file.Close()
		return nil, ErrIsolation
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 {
		_ = file.Close()
		return nil, ErrIsolation
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func recoverGroups(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "space-p1-") {
			continue
		}
		if err := recoverGroup(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func recoverGroup(group string) error {
	children, err := recoveryChildren(group)
	if err != nil {
		return err
	}
	if err := killGroup(group); err != nil {
		return err
	}
	for _, child := range children {
		if err := removeGroup(child); err != nil {
			return err
		}
	}
	return removeGroup(group)
}

// Неожиданная структура отклоняется до уничтожения процессов этой группы.
func recoveryChildren(group string) ([]string, error) {
	entries, err := os.ReadDir(group)
	if err != nil {
		return nil, err
	}
	var children []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !strings.HasPrefix(entry.Name(), "call-") {
			return nil, ErrIsolation
		}
		child := filepath.Join(group, entry.Name())
		if err := requireLeaf(child); err != nil {
			return nil, err
		}
		children = append(children, child)
	}
	return children, nil
}

func requireLeaf(group string) error {
	entries, err := os.ReadDir(group)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return ErrIsolation
		}
	}
	return nil
}
