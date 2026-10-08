package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"github.com/space-protocol-project/space-protocol/server/internal/chat"
	"github.com/space-protocol-project/space-protocol/server/internal/postgres"
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
	grpcAddress := flag.String("grpc", "127.0.0.1:9090", "Локальный gRPC адрес")
	demo := flag.Bool("demo", false, "Явно использовать временное хранилище в памяти")
	firstOwner := flag.Bool("first-owner", false, "Включить назначение владельца первым входом для ненастроенного сервера и завершиться")
	setupCode := flag.Bool("setup-code", false, "Выдать/заменить одноразовый код первого владельца и завершиться")
	originFlag := flag.String("origin", "", "Origin браузера, в том числе локальная сторона SSH-туннеля")
	flag.Parse()
	origin := "http://" + *port
	if *originFlag != "" {
		origin = *originFlag
	}
	parsed, parseErr := url.Parse(origin)
	if parseErr != nil || parsed.Scheme != "http" || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("origin должен быть локальным HTTP origin без пути")
	}
	parsed.Path = ""
	if parsed.Port() == "80" {
		parsed.Host = parsed.Hostname()
		if strings.Contains(parsed.Host, ":") {
			parsed.Host = "[" + parsed.Host + "]"
		}
	}
	origin = parsed.String()
	host, _, err := net.SplitHostPort(*port)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("прототип без авторизации разрешает только IP loopback, например 127.0.0.1:8080")
	}
	grpcHost, _, err := net.SplitHostPort(*grpcAddress)
	if err != nil || net.ParseIP(grpcHost) == nil || !net.ParseIP(grpcHost).IsLoopback() {
		return fmt.Errorf("gRPC адрес должен быть loopback IP")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	service, identity, store, err := openService(ctx, *demo)
	if err != nil {
		return err
	}
	if store != nil {
		defer store.Close()
	}
	if *firstOwner {
		if *setupCode || store == nil {
			return fmt.Errorf("first-owner требует PostgreSQL и не совмещается с setup-code")
		}
		if err := store.EnableFirstOwner(ctx); err != nil {
			return err
		}
		fmt.Println("Первый успешный вход назначит владельца")
		return nil
	}
	if *setupCode {
		if store == nil {
			return fmt.Errorf("код настройки требует PostgreSQL")
		}
		limited, cancelCode := context.WithTimeout(ctx, 15*time.Second)
		defer cancelCode()
		code, err := store.CreateSetupCode(limited)
		if err != nil {
			return err
		}
		fmt.Println(code)
		return nil
	}
	listener, err := net.Listen("tcp", *grpcAddress)
	if err != nil {
		return err
	}
	options := []grpc.ServerOption{grpc.MaxRecvMsgSize(16 * 1024)}
	if store != nil {
		options = append(options, grpc.UnaryInterceptor(authn.Interceptor(store)), grpc.StreamInterceptor(authn.StreamInterceptor(store)))
	}
	server := grpc.NewServer(options...)
	pb.RegisterChannelServiceServer(server, service)
	pb.RegisterContentServiceServer(server, service)
	if store != nil {
		pb.RegisterAuthServiceServer(server, postgres.NewAuth(store, identity.ServerID, origin))
		pb.RegisterSyncServiceServer(server, store)
		pb.RegisterAdminServiceServer(server, store)
		pb.RegisterMembershipServiceServer(server, store)
	}
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
	handler, err := transport.HandlerWithEndpoint(ctx, connection, identity.ServerID, identity.PublicKey, listener.Addr().String())
	if err != nil {
		return err
	}
	handler = transport.RestrictOrigin(handler, origin)
	httpServer := &http.Server{Addr: *port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	failures := make(chan error, 1)
	go func() { failures <- httpServer.ListenAndServe() }()
	log.Printf("Локальный прототип: http://%s; server_id=%s; demo=%t", *port, identity.ServerID, *demo)
	select {
	case err := <-failures:
		return err
	case <-ctx.Done():
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		return httpServer.Shutdown(shutdown)
	}
}

func openService(ctx context.Context, demo bool) (*chat.Service, postgres.Identity, *postgres.Store, error) {
	if demo {
		return chat.New("local-prototype"), postgres.Identity{ServerID: "local-prototype"}, nil, nil
	}
	url := os.Getenv("SPACE_DATABASE_URL")
	if url == "" {
		return nil, postgres.Identity{}, nil, fmt.Errorf("нужна SPACE_DATABASE_URL; для временного режима используйте -demo")
	}
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	store, identity, err := postgres.Open(startup, url)
	if err != nil {
		return nil, postgres.Identity{}, nil, err
	}
	return chat.NewPersistent(identity.ServerID, store), identity, store, nil
}
