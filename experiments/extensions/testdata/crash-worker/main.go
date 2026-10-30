//go:build linux

// Только стенд CI: настоящий Worker останавливается перед выдачей результата.
package main

import (
	"os"
	"syscall"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
)

type stoppedOutput struct{}

func (stoppedOutput) Write(data []byte) (int, error) {
	if err := syscall.Kill(os.Getpid(), syscall.SIGSTOP); err != nil {
		return 0, err
	}
	return os.Stdout.Write(data)
}

func main() {
	if err := extensions.Worker(os.Stdin, stoppedOutput{}); err != nil {
		os.Exit(1)
	}
}
