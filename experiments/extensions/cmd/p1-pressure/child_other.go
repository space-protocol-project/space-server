//go:build !windows

package main

import "os/exec"

func configureChild(child *exec.Cmd) {}
