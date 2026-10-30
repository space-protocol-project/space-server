// Только loopback-стенд CI; не служба рабочего сервера.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	root := flag.String("cgroup", "", "cgroup root")
	worker := flag.String("worker", "", "worker executable")
	module := flag.String("module", "testdata/demo.wasm", "demo module")
	manifestPath := flag.String("manifest", "demo/manifest.json", "demo manifest")
	broken := flag.String("broken", "testdata/loop.wasm", "failure fixture")
	credentials := flag.String("credentials", "", "CI credentials JSON")
	address := flag.String("address-file", "", "ready address file")
	flag.Parse()
	raw, err := os.ReadFile(*credentials)
	if err != nil {
		return err
	}
	var tokens struct{ Editor, Reader string }
	if json.Unmarshal(raw, &tokens) != nil || len(tokens.Editor) < 32 || len(tokens.Reader) < 32 || tokens.Editor == tokens.Reader {
		return errors.New("invalid CI credentials")
	}
	authorize := func(ctx context.Context, token, instance string, command bool) (demo.Principal, error) {
		editor := subtle.ConstantTimeCompare([]byte(token), []byte(tokens.Editor)) == 1
		reader := subtle.ConstantTimeCompare([]byte(token), []byte(tokens.Reader)) == 1
		if (!editor && !reader) || (command && !editor) || ctx.Err() != nil {
			return demo.Principal{}, demo.ErrDenied
		}
		user := "reader"
		if editor {
			user = "editor"
		}
		return demo.Principal{Space: "space_demo", Instance: instance, User: user, Device: "ci_device", Revision: "1"}, nil
	}
	raw, err = os.ReadFile(*manifestPath)
	if err != nil {
		return err
	}
	var manifest demo.Manifest
	if json.Unmarshal(raw, &manifest) != nil {
		return demo.ErrRequest
	}
	wasm, err := os.ReadFile(*module)
	if err != nil {
		return err
	}
	bad, err := os.ReadFile(*broken)
	if err != nil {
		return err
	}
	host, err := extensions.NewProcessHost(*root, *worker)
	if err != nil {
		return err
	}
	defer host.Close()
	dispatcher, err := demo.New(host, authorize, map[string]demo.Registration{"demo": {Manifest: manifest, Module: wasm}, "broken": {Manifest: manifest, Module: bad}})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: dispatcher.Handler(), ReadHeaderTimeout: time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if err = os.WriteFile(*address, []byte("http://"+listener.Addr().String()+"/dispatch"), 0644); err != nil {
		return err
	}
	if err = os.WriteFile(*address+".pid", []byte(fmt.Sprint(os.Getpid())), 0644); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}
