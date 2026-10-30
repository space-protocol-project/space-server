package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
)

func main() {
	worker := flag.String("worker", "", "absolute path to trusted worker")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := extensions.Service(ctx, *worker); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
