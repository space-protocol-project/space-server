//go:build !windows

package home

import "os"

func protectBackupDirectory(path string) error { return os.Chmod(path, 0700) }
