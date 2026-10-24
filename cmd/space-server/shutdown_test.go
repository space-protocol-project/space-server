package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	health "google.golang.org/grpc/health/grpc_health_v1"
)

type shutdownHealth struct {
	health.UnimplementedHealthServer
	entered            chan struct{}
	release            chan struct{}
	watching           chan struct{}
	watchDone          chan struct{}
	completed          atomic.Int32
	ignoreCancellation bool
}

func (service *shutdownHealth) Check(ctx context.Context, _ *health.HealthCheckRequest) (*health.HealthCheckResponse, error) {
	service.entered <- struct{}{}
	if service.ignoreCancellation {
		<-service.release
		return &health.HealthCheckResponse{}, nil
	}
	select {
	case <-service.release:
		service.completed.Add(1)
		return &health.HealthCheckResponse{Status: health.HealthCheckResponse_SERVING}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (service *shutdownHealth) Watch(_ *health.HealthCheckRequest, stream health.Health_WatchServer) error {
	close(service.watching)
	<-stream.Context().Done()
	close(service.watchDone)
	return stream.Context().Err()
}

func shutdownBridge(t *testing.T, ctx context.Context) (*rpcBridge, *shutdownHealth) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.ChainStreamInterceptor(stopStreamsWith(ctx)))
	service := &shutdownHealth{entered: make(chan struct{}, 2), release: make(chan struct{}), watching: make(chan struct{}), watchDone: make(chan struct{})}
	health.RegisterHealthServer(server, service)
	go server.Serve(listener)
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	bridge := &rpcBridge{listener, server, connection}
	t.Cleanup(bridge.close)
	return bridge, service
}

func awaitShutdownEvent(t *testing.T, event <-chan struct{}) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown event timed out")
	}
}

func TestDrainBothHTTPServersKeepsRPCAliveForAcceptedRequests(t *testing.T) {
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge, service := shutdownBridge(t, lifecycle)
	client := health.NewHealthClient(bridge.connection)
	requestResults := make(chan error, 2)
	servers := make([]*http.Server, 0, 2)
	draining := make(chan struct{}, 2)
	for range 2 {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := client.Check(r.Context(), &health.HealthCheckRequest{})
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			_, _ = w.Write([]byte("committed"))
		}))
		t.Cleanup(server.Close)
		server.Config.RegisterOnShutdown(func() { draining <- struct{}{} })
		servers = append(servers, server.Config)
		go func() {
			response, err := server.Client().Get(server.URL)
			if err == nil {
				defer response.Body.Close()
				body, readErr := io.ReadAll(response.Body)
				err = readErr
				if string(body) != "committed" {
					err = errors.New("accepted request lost")
				}
			}
			requestResults <- err
		}()
	}
	awaitShutdownEvent(t, service.entered)
	awaitShutdownEvent(t, service.entered)
	_, err := client.Watch(context.Background(), &health.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	awaitShutdownEvent(t, service.watching)
	cancel()
	ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	result := make(chan error, 1)
	go func() { result <- drainServers(ctx, bridge, servers...) }()
	awaitShutdownEvent(t, draining)
	awaitShutdownEvent(t, draining)
	awaitShutdownEvent(t, service.watchDone)
	select {
	case err := <-result:
		t.Fatalf("drained before accepted requests: %v", err)
	default:
	}
	close(service.release)
	for range 2 {
		if err := <-requestResults; err != nil {
			t.Fatal(err)
		}
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if service.completed.Load() != 2 {
		t.Fatal("accepted RPCs did not complete")
	}
}

func TestDrainTimeoutCancelsHTTPAndRPC(t *testing.T) {
	bridge, service := shutdownBridge(t, context.Background())
	client := health.NewHealthClient(bridge.connection)
	requestDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(requestDone)
		_, _ = client.Check(r.Context(), &health.HealthCheckRequest{})
	}))
	defer server.Close()
	go func() {
		response, err := server.Client().Get(server.URL)
		if err == nil {
			response.Body.Close()
		}
	}()
	awaitShutdownEvent(t, service.entered)
	_, err := client.Watch(context.Background(), &health.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	awaitShutdownEvent(t, service.watching)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := drainServers(ctx, bridge, server.Config); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("deadline did not bound shutdown")
	}
	awaitShutdownEvent(t, requestDone)
	awaitShutdownEvent(t, service.watchDone)
	if service.completed.Load() != 0 {
		t.Fatal("unfinished RPC was reported as completed")
	}
}

func TestRPCDrainDoesNotWaitForeverForHandlerIgnoringCancellation(t *testing.T) {
	bridge, service := shutdownBridge(t, context.Background())
	service.ignoreCancellation = true
	defer close(service.release)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		_, _ = health.NewHealthClient(bridge.connection).Check(context.Background(), &health.HealthCheckRequest{})
	}()
	awaitShutdownEvent(t, service.entered)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- drainRPC(ctx, bridge.server) }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("non-cooperative handler blocked shutdown")
	}
	awaitShutdownEvent(t, requestDone)
}

func TestManagedEOFStopsServerProcess(t *testing.T) {
	if os.Getenv("SPACE_SHUTDOWN_TEST_CHILD") == "1" {
		log.SetOutput(os.Stdout)
		if err := runServer(serverOptions{demo: true, managed: true, port: "127.0.0.1:0", grpcAddress: "127.0.0.1:0"}); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedEOFStopsServerProcess$")
	cmd.Env = append(os.Environ(), "SPACE_SHUTDOWN_TEST_CHILD=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || !strings.Contains(scanner.Text(), "demo=true") {
		t.Fatalf("server did not start: %s (%v)", scanner.Text(), scanner.Err())
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("managed EOF: %v", err)
	}
}

func TestCleanupTimeoutIsReported(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := waitForCleanup(ctx, "worker", func() { <-release }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup timeout=%v", err)
	}
	if err := waitForCleanup(context.Background(), "worker", func() {}); err != nil {
		t.Fatal(err)
	}
}
