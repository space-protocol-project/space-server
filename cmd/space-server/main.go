package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"github.com/space-protocol-project/space-protocol/server/internal/chat"
	"github.com/space-protocol-project/space-protocol/server/internal/home"
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
	homeData := flag.String("home-data", "", "Каталог постоянных данных домашнего сервера")
	homeRuntime := flag.String("home-runtime", "", "Каталог проверенного выпуска с PostgreSQL")
	publicIP := flag.String("public-ip", "", "Статический публичный IP домашнего сервера")
	httpsAddress := flag.String("https", "0.0.0.0:8443", "HTTPS/gRPC адрес домашнего сервера")
	managed := flag.Bool("managed", false, "Завершаться при закрытии stdin управляющего приложения")
	flag.Parse()
	homeMode := *homeData != ""
	var certificate tls.Certificate
	var publicOrigin string
	if homeMode {
		ip := net.ParseIP(*publicIP)
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || *homeRuntime == "" || *demo || *originFlag != "" || *setupCode || *firstOwner {
			return fmt.Errorf("Домашнему серверу нужны публичный IP, runtime и постоянные данные")
		}
		_, httpsPort, err := net.SplitHostPort(*httpsAddress)
		if err != nil || httpsPort == "0" {
			return fmt.Errorf("Некорректный HTTPS порт")
		}
		publicOrigin = "https://" + net.JoinHostPort(ip.String(), httpsPort)
		databaseURL, stopDatabase, err := home.StartDatabase(*homeRuntime, *homeData)
		if err != nil {
			return err
		}
		defer stopDatabase()
		if err := os.Setenv("SPACE_DATABASE_URL", databaseURL); err != nil {
			return err
		}
		certificate, err = home.Certificate(*homeData, ip)
		if err != nil {
			return err
		}
	} else if *publicIP != "" || *homeRuntime != "" {
		return fmt.Errorf("Нужен home-data")
	}

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
	if *managed {
		go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
	}
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
	setup := ""
	if homeMode {
		status, err := store.GetSetupStatus(ctx, &pb.GetSetupStatusRequest{})
		if err != nil {
			return err
		}
		if !status.Initialized {
			setup, err = store.CreateSetupCode(ctx)
			if err != nil {
				return err
			}
		}
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
		pb.RegisterAuthServiceServer(server, postgres.NewAuth(store, identity.ServerID, authOrigin(origin, publicOrigin)))
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
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	failures := make(chan error, 2)
	localListener, err := net.Listen("tcp", *port)
	if err != nil {
		return err
	}
	defer localListener.Close()
	go func() { failures <- httpServer.Serve(localListener) }()
	defer httpServer.Close()
	if homeMode {
		// Внешние запросы запрещены до назначения владельца через локальный вход.
		var initialized atomic.Bool
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				status, err := store.GetSetupStatus(ctx, &pb.GetSetupStatusRequest{})
				if err == nil && status.Initialized {
					initialized.Store(true)
					return
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					return
				}
			}
		}()
		endpoint, _ := url.Parse(publicOrigin)
		publicHandler, err := transport.HandlerWithEndpoint(ctx, connection, identity.ServerID, identity.PublicKey, endpoint.Host)
		if err != nil {
			return err
		}
		publicHandler = transport.RestrictOrigin(publicHandler, publicOrigin)
		combined := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !initialized.Load() {
				http.Error(w, "Сначала настройте владельца локально", http.StatusServiceUnavailable)
				return
			}
			if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
				if !strings.EqualFold(r.Host, endpoint.Host) {
					http.Error(w, "Недопустимый Host", http.StatusForbidden)
					return
				}
				server.ServeHTTP(w, r)
				return
			}
			publicHandler.ServeHTTP(w, r)
		})
		secureServer := &http.Server{Addr: *httpsAddress, Handler: combined, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}}
		secureListener, err := net.Listen("tcp", *httpsAddress)
		if err != nil {
			return err
		}
		defer secureListener.Close()
		defer secureServer.Close()
		go func() { failures <- secureServer.ServeTLS(secureListener, "", "") }()
		state, err := json.Marshal(map[string]string{"local_origin": origin, "public_origin": publicOrigin, "setup_code": setup})
		if err != nil {
			return err
		}
		statePath := filepath.Join(*homeData, "state.json")
		if err := os.WriteFile(statePath+".tmp", state, 0600); err != nil {
			return err
		}
		_ = os.Remove(statePath)
		if err := os.Rename(statePath+".tmp", statePath); err != nil {
			return err
		}
		defer os.Remove(statePath)
	}
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

func authOrigin(local, public string) string {
	if public != "" {
		return public
	}
	return local
}
