//go:build !windows

package home

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
