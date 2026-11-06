package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureChild(child *exec.Cmd) {
	// Потомок, как и исходный worker, не создаёт консольный conhost.
	child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
