package main

import (
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"os"
)

func main() {
	if extensions.Worker(os.Stdin, os.Stdout) != nil {
		os.Exit(1)
	}
}
