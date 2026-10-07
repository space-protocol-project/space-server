package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/chat"
	"github.com/space-protocol-project/space-protocol/server/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	port := flag.String("http", "127.0.0.1:8080", "Локальный HTTP адрес")
	flag.Parse()
	host, _, err := net.SplitHostPort(*port)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("прототип без авторизации разрешает только IP loopback, например 127.0.0.1:8080")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	server := grpc.NewServer(grpc.MaxRecvMsgSize(16 * 1024))
	service := chat.New("local-prototype")
	pb.RegisterChannelServiceServer(server, service)
	pb.RegisterContentServiceServer(server, service)
	go func() {
		if err := server.Serve(listener); err != nil {
			log.Printf("gRPC: %v", err)
			cancel()
		}
	}()
	defer server.Stop()
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer connection.Close()
	handler, err := transport.Handler(ctx, connection, "local-prototype")
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: *port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	failures := make(chan error, 1)
	go func() { failures <- httpServer.ListenAndServe() }()
	log.Printf("Локальный прототип: http://%s; сообщения хранятся в памяти", *port)
	select {
	case err := <-failures:
		return err
	case <-ctx.Done():
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		return httpServer.Shutdown(shutdown)
	}
}
