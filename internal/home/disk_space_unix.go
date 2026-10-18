//go:build linux || darwin

package home

import "golang.org/x/sys/unix"

func scheduledFreeBytes(directory string) (uint64, error) {
	var info unix.Statfs_t
	if err := unix.Statfs(directory, &info); err != nil {
		return 0, err
	}
	return uint64(info.Bavail) * uint64(info.Bsize), nil
}
