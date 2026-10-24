package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"google.golang.org/grpc"
)

const requestDrainTimeout = 10 * time.Second

// Отдельный срок нужен и после drain: сбой фоновой задачи не оставляет процесс висеть.
func boundedCleanup(name string, cleanup func()) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return waitForCleanup(ctx, name, cleanup)
}

func waitForCleanup(ctx context.Context, name string, cleanup func()) error {
	done := make(chan struct{})
	go func() { cleanup(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.Join(errors.New(name), ctx.Err())
	}
}

// Оба HTTP входа завершают принятые запросы, пока внутренний gRPC ещё доступен.
func drainServers(ctx context.Context, bridge *rpcBridge, servers ...*http.Server) error {
	results := make(chan error, len(servers))
	for _, server := range servers {
		go func() {
			err := server.Shutdown(ctx)
			if err != nil {
				err = errors.Join(err, server.Close())
			}
			results <- err
		}()
	}
	var result error
	for range servers {
		result = errors.Join(result, <-results)
	}
	return errors.Join(result, drainRPC(ctx, bridge.server))
}

func drainRPC(ctx context.Context, server *grpc.Server) error {
	done := make(chan struct{})
	go func() { server.GracefulStop(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// GracefulStop может держать mutex, ожидая обработчик без реакции на
		// отмену. Stop запускается отдельно: срок не зависит от этого mutex.
		go server.Stop()
		return ctx.Err()
	}
}

type lifecycleStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (stream lifecycleStream) Context() context.Context { return stream.ctx }

// Подписки не имеют естественного конца: остановка отменяет их, но не unary RPC.
func stopStreamsWith(lifecycle context.Context) grpc.StreamServerInterceptor {
	return func(service any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, cancel := context.WithCancel(stream.Context())
		stop := context.AfterFunc(lifecycle, cancel)
		defer stop()
		defer cancel()
		return handler(service, lifecycleStream{ServerStream: stream, ctx: ctx})
	}
}
