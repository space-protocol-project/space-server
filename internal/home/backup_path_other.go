//go:build !windows

package home

import "path/filepath"

func finalBackupPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
