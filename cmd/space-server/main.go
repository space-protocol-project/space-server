package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
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
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/chat"
	"github.com/space-protocol-project/space-server/internal/home"
	"github.com/space-protocol-project/space-server/internal/postgres"
	"github.com/space-protocol-project/space-server/internal/transport"
	"github.com/space-protocol-project/space-server/internal/voice"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "start" {
		return runStandalone(os.Args[2:], os.Stdout)
	}
	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		return runDoctor(os.Args[2:], os.Stdout)
	}
	options := parseOptions()
	if handled, err := performMaintenance(options); handled {
		return err
	}
	if err := applyHomeNetwork(&options); err != nil {
		return err
	}
	return runServer(options)
}

func runServer(options serverOptions) (result error) {
	homeMode := options.homeData != ""
	certificate, publicOrigin, stopDatabase, err := prepareHomeRuntime(options)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, stopDatabase()) }()

	origin, err := localOrigin(options.port, options.originFlag)
	if err != nil {
		return err
	}
	if err := validateLocalAddresses(options); err != nil {
		return err
	}
	ctx, cancel := serverContext(options.managed)
	defer cancel()
	service, identity, store, err := openService(ctx, options.demo)
	if err != nil {
		return err
	}
	if store != nil {
		defer func() { result = errors.Join(result, boundedCleanup("PostgreSQL pool", store.Close)) }()
	}
	if handled, err := configureOwner(ctx, options, store); handled {
		return err
	}
	pendingSetup := filepath.Join(options.homeData, "bootstrap-pending")
	setup, err := prepareHomeSetup(ctx, store, homeMode, pendingSetup)
	if err != nil {
		return err
	}
	identityOrigin, err := homeIdentityOrigin(options, publicOrigin)
	if err != nil {
		return err
	}
	bridge, err := startRPCBridge(ctx, options, service, store, identity, origin, identityOrigin, cancel)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, boundedCleanup("gRPC bridge", bridge.close)) }()
	listener, connection := bridge.listener, bridge.connection
	homeConfig := homeHTTPConfig{
		address: options.httpsAddress, certificate: certificate, data: options.homeData,
		localOrigin: origin, publicOrigin: publicOrigin, identityOrigin: identityOrigin, setupCode: setup,
		pendingSetupPath: pendingSetup, statePath: filepath.Join(options.homeData, "state.json"),
		remoteSetup: options.remoteSetup, output: options.output,
	}
	handler, err := localHTTPHandler(ctx, connection, store, identity, homeConfig, listener.Addr().String())
	if err != nil {
		return err
	}

	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	failures := make(chan error, 2)
	localListener, err := net.Listen("tcp", options.port)
	if err != nil {
		return err
	}
	defer localListener.Close()
	go func() { failures <- httpServer.Serve(localListener) }()
	defer httpServer.Close()
	servers := []*http.Server{httpServer}
	if homeMode {
		publicServer, err := startPublicServer(ctx, homeConfig, connection, store, identity, failures)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, boundedCleanup("public HTTP resources", publicServer.close)) }()
		servers = append(servers, publicServer.server)
	}
	log.Printf("Локальный прототип: http://%s; server_id=%s; demo=%t", options.port, identity.ServerID, options.demo)
	if homeMode && !options.demo {
		stopBackups := home.StartScheduledBackups(ctx, home.ScheduledBackupOptions{Data: options.homeData, Runtime: options.homeRuntime, Address: os.Getenv("SPACE_DATABASE_URL"), ServerID: identity.ServerID})
		defer func() { result = errors.Join(result, boundedCleanup("scheduled backups", stopBackups)) }()
	}
	select {
	case result = <-failures:
		if errors.Is(result, http.ErrServerClosed) {
			result = nil
		}
	case <-ctx.Done():
	}
	cancel()
	shutdown, done := context.WithTimeout(context.Background(), requestDrainTimeout)
	defer done()
	return errors.Join(result, drainServers(shutdown, bridge, servers...))
}

// При ошибке подготовки база закрывается здесь; после успеха её закрывает run.
func prepareHomeRuntime(options serverOptions) (tls.Certificate, string, func() error, error) {
	var certificate tls.Certificate
	noop := func() error { return nil }
	if options.homeData == "" {
		if options.publicIP != "" || options.homeRuntime != "" {
			return certificate, "", noop, fmt.Errorf("Нужен home-data")
		}
		return certificate, "", noop, nil
	}
	_, origin, err := homeEndpoint(options)
	if err != nil {
		return certificate, "", noop, err
	}
	databaseURL, stopDatabase, err := home.StartDatabase(options.homeRuntime, options.homeData)
	if err != nil {
		return certificate, "", noop, err
	}
	if err := os.Setenv("SPACE_DATABASE_URL", databaseURL); err != nil {
		return certificate, "", noop, errors.Join(err, stopDatabase())
	}
	certificate, err = homeCertificate(options.homeData, origin)
	if err != nil {
		return certificate, "", noop, errors.Join(err, stopDatabase())
	}
	return certificate, origin, stopDatabase, nil
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateLocalAddresses(options serverOptions) error {
	if !isLoopbackAddress(options.port) {
		return fmt.Errorf("прототип без авторизации разрешает только IP loopback, например 127.0.0.1:8080")
	}
	if !isLoopbackAddress(options.grpcAddress) {
		return fmt.Errorf("gRPC адрес должен быть loopback IP")
	}
	return nil
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

func finishHomeSetup(ctx context.Context, store interface{ SealHomeSetup(context.Context) error }, path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := store.SealHomeSetup(ctx); err != nil {
		return err
	}
	return os.Remove(path)
}

type serverOptions struct {
	portableVerify                             string
	remoteSetup                                bool
	output                                     io.Writer
	portableExport, portableImport, portableTo string
	port                                       string
	grpcAddress                                string
	demo                                       bool
	firstOwner                                 bool
	setupCode                                  bool
	originFlag                                 string
	homeData                                   string
	homeRuntime                                string
	publicIP                                   string
	httpsAddress                               string
	httpsSpecified                             bool
	managed                                    bool
	runtimeInfo                                bool
	checkDatabase                              bool
	homeCheck                                  bool
	homeMigrate                                bool
	homeStopped                                bool
	backup                                     string
	restore                                    string
	restoreTo                                  string
}

func parseOptions() serverOptions {
	portableVerify := flag.String("home-verify-export", "", "Проверить архив переноса без распаковки и запуска базы")
	portableExport := flag.String("home-export", "", "Экспорт пространства в переносимый ZIP")
	portableImport := flag.String("home-import", "", "Импорт переносимого архива пространства")
	portableTo := flag.String("home-import-to", "", "Новый каталог для импортируемого пространства")
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
	runtimeInfo := flag.Bool("runtime-info", false, "Показать совместимость runtime без запуска сервера")
	checkDatabase := flag.Bool("check-database", false, "Проверить существующую домашнюю базу только чтением, без миграций")
	homeCheck := flag.Bool("home-check", false, "Проверить остановленную домашнюю базу без публичного входа")
	homeMigrate := flag.Bool("home-migrate", false, "Применить миграции остановленной домашней базы без публичного входа")
	homeStopped := flag.Bool("home-stopped", false, "Подтвердить чистую остановку PostgreSQL без проверки схемы")
	backup := flag.String("home-backup", "", "Создать копию остановленного домашнего сервера в новом каталоге")
	restore := flag.String("home-restore", "", "Путь проверенной резервной копии для восстановления")
	restoreTo := flag.String("home-restore-to", "", "Новый корневой каталог домашнего сервера для восстановления")
	flag.Parse()
	httpsSpecified := false
	flag.Visit(func(value *flag.Flag) {
		if value.Name == "https" {
			httpsSpecified = true
		}
	})
	return serverOptions{
		portableVerify: *portableVerify,
		portableExport: *portableExport, portableImport: *portableImport, portableTo: *portableTo, port: *port,
		grpcAddress:    *grpcAddress,
		demo:           *demo,
		firstOwner:     *firstOwner,
		setupCode:      *setupCode,
		originFlag:     *originFlag,
		homeData:       *homeData,
		homeRuntime:    *homeRuntime,
		publicIP:       *publicIP,
		httpsAddress:   *httpsAddress,
		httpsSpecified: httpsSpecified,
		managed:        *managed,
		runtimeInfo:    *runtimeInfo,
		checkDatabase:  *checkDatabase,
		homeCheck:      *homeCheck,
		homeMigrate:    *homeMigrate,
		homeStopped:    *homeStopped,
		backup:         *backup,
		restore:        *restore,
		restoreTo:      *restoreTo,
	}
}

func performMaintenance(options serverOptions) (bool, error) {
	if options.portableVerify != "" {
		return true, performPortableInspection(options)
	}
	if options.portableExport != "" || options.portableImport != "" || options.portableTo != "" {
		return true, performPortableTransfer(options)
	}
	if options.homeCheck || options.homeMigrate || options.homeStopped {
		return true, performHomeCheck(options)
	}
	if options.backup != "" || options.restore != "" || options.restoreTo != "" {
		return true, performHomeBackup(options)
	}
	if options.runtimeInfo && options.checkDatabase {
		return true, errors.New("Выберите одну проверку совместимости")
	}
	if options.runtimeInfo {
		return true, json.NewEncoder(os.Stdout).Encode(map[string]any{
			"portableInspectionVersion": 1,
			"scheduledBackupsVersion":   1,
			"voiceRoomsVersion":         3,
			"remoteSetupVersion":        1,
			"automaticNetworkVersion":   1, "networkDoctorVersion": 2, "endpointSetVersion": 1, "portableTransferVersion": 1, "format": 1, "protocolVersion": "0.1-experimental", "schemaVersion": postgres.SchemaVersion(),
			"postgresMajor": postgres.HomePostgresMajor, "os": runtime.GOOS, "arch": runtime.GOARCH,
		})
	}
	if options.checkDatabase {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		result, err := postgres.CheckCompatibility(ctx, os.Getenv("SPACE_DATABASE_URL"))
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(os.Stdout).Encode(result)
	}
	return false, nil
}

func localOrigin(address, override string) (string, error) {
	origin := "http://" + address
	if override != "" {
		origin = override
	}
	parsed, parseErr := url.Parse(origin)
	if parseErr != nil || parsed.Scheme != "http" || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("origin должен быть локальным HTTP origin без пути")
	}
	parsed.Path = ""
	if parsed.Port() == "80" {
		parsed.Host = parsed.Hostname()
		if strings.Contains(parsed.Host, ":") {
			parsed.Host = "[" + parsed.Host + "]"
		}
	}
	origin = parsed.String()
	return origin, nil
}

func prepareHomeSetup(ctx context.Context, store *postgres.Store, enabled bool, pendingPath string) (string, error) {
	if !enabled {
		return "", nil
	}
	setup, err := store.GetSetupStatus(ctx, &pb.GetSetupStatusRequest{})
	if err != nil || setup.Initialized {
		return "", err
	}
	code, err := store.CreateSetupCode(ctx)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(pendingPath, []byte("private setup pending"), 0600); err != nil {
		return "", err
	}
	return code, nil
}

// Публичный вход открывается после назначения владельца и закрытия настройки.
func watchHomeSetup(ctx context.Context, store *postgres.Store, pendingPath string) *atomic.Bool {
	initialized := &atomic.Bool{}
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			setup, err := store.GetSetupStatus(ctx, &pb.GetSetupStatusRequest{})
			if err == nil && setup.Initialized && finishHomeSetup(ctx, store, pendingPath) == nil {
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
	return initialized
}

func writeHomeState(path, localOrigin, publicOrigin, setupCode string, serverIDs ...string) error {
	document := map[string]string{
		"local_origin": localOrigin, "public_origin": publicOrigin, "setup_code": setupCode,
	}
	if len(serverIDs) > 0 {
		document["server_id"] = serverIDs[0]
	}
	state, err := json.Marshal(document)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", state, 0600); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(path+".tmp", path)
}

func performHomeCheck(options serverOptions) error {
	if options.homeCheck && options.homeMigrate || options.homeStopped && (options.homeCheck || options.homeMigrate) || options.homeData == "" || options.homeRuntime == "" || options.backup != "" || options.restore != "" || options.restoreTo != "" || options.runtimeInfo || options.checkDatabase {
		return errors.New("Для закрытой проверки или миграции нужны home-data, home-runtime и одна операция")
	}
	if options.homeStopped {
		return home.CheckStoppedDatabase(options.homeRuntime, options.homeData)
	}
	result, err := home.CheckOrMigrate(options.homeRuntime, options.homeData, options.homeMigrate)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func performHomeBackup(options serverOptions) error {
	if options.backup != "" && options.restore != "" || options.runtimeInfo || options.checkDatabase || options.homeRuntime == "" {
		return errors.New("Для копии или восстановления нужен home-runtime и одна операция")
	}
	if options.backup != "" {
		if options.homeData == "" || options.restoreTo != "" {
			return errors.New("Для копии нужен home-data")
		}
		return home.CreateBackup(options.homeRuntime, options.homeData, options.backup)
	}
	if options.restore == "" || options.restoreTo == "" || options.homeData != "" {
		return errors.New("Для восстановления нужны home-restore и home-restore-to без home-data")
	}
	return home.RestoreBackup(options.homeRuntime, options.restore, options.restoreTo)
}

func configureOwner(ctx context.Context, options serverOptions, store *postgres.Store) (bool, error) {
	if options.firstOwner {
		if options.setupCode || store == nil {
			return true, fmt.Errorf("first-owner требует PostgreSQL и не совмещается с setup-code")
		}
		if err := store.EnableFirstOwner(ctx); err != nil {
			return true, err
		}
		fmt.Println("Первый успешный вход назначит владельца")
		return true, nil
	}
	if options.setupCode {
		if store == nil {
			return true, fmt.Errorf("код настройки требует PostgreSQL")
		}
		limited, cancelCode := context.WithTimeout(ctx, 15*time.Second)
		defer cancelCode()
		code, err := store.CreateSetupCode(limited)
		if err != nil {
			return true, err
		}
		fmt.Println(code)
		return true, nil
	}
	return false, nil
}

func homeEndpoint(options serverOptions) (net.IP, string, error) {
	config, err := home.ReadNetworkConfig(options.homeData)
	if err != nil {
		return nil, "", err
	}
	if len(config.Endpoints) > 0 {
		if options.homeRuntime == "" || options.demo || options.originFlag != "" || options.setupCode || options.firstOwner {
			return nil, "", fmt.Errorf("Некорректный режим домашнего сервера")
		}
		return net.ParseIP(config.Endpoints[0].Host), config.Endpoints[0].Origin(), nil
	}
	ip := net.ParseIP(options.publicIP)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || options.homeRuntime == "" || options.demo || options.originFlag != "" || options.setupCode || options.firstOwner {
		return nil, "", fmt.Errorf("Домашнему серверу нужны публичный IP, runtime и постоянные данные")
	}
	_, httpsPort, err := net.SplitHostPort(options.httpsAddress)
	if err != nil || httpsPort == "0" {
		return nil, "", fmt.Errorf("Некорректный HTTPS порт")
	}
	publicOrigin := "https://" + net.JoinHostPort(ip.String(), httpsPort)
	return ip, publicOrigin, nil
}

func newRPCServer(service *chat.Service, store *postgres.Store, serverID, origin string) *grpc.Server {
	voiceService, err := voice.NewService(context.Background(), store)
	if err != nil {
		panic(err)
	}
	return configuredRPCServer(service, store, serverID, origin, voiceService)
}

func configuredRPCServer(service *chat.Service, store *postgres.Store, serverID, origin string, voiceService *voice.Service, lifecycle ...grpc.StreamServerInterceptor) *grpc.Server {
	grpcOptions := []grpc.ServerOption{grpc.MaxRecvMsgSize(transport.MaxRPCRequestBytes)}
	unary := []grpc.UnaryServerInterceptor{transport.MessageSizeLimit}
	streams := append(lifecycle, transport.StreamMessageSizeLimit)
	if store != nil {
		unary = append(unary, authn.Interceptor(store))
		streams = append(streams, authn.StreamInterceptor(store))
	}
	grpcOptions = append(grpcOptions, grpc.ChainUnaryInterceptor(unary...), grpc.ChainStreamInterceptor(streams...))
	server := grpc.NewServer(grpcOptions...)
	pb.RegisterChannelServiceServer(server, service)
	pb.RegisterContentServiceServer(server, service)
	if store != nil {
		pb.RegisterVoiceServiceServer(server, voiceService)
		pb.RegisterForumServiceServer(server, store)
		pb.RegisterFeedServiceServer(server, store)
		pb.RegisterCommunitySearchServiceServer(server, store)
		pb.RegisterSpaceSearchServiceServer(server, store)
		pb.RegisterParticipantFollowServiceServer(server, store)
		pb.RegisterUserExportServiceServer(server, store)
		pb.RegisterCommunityOverviewServiceServer(server, store)
		pb.RegisterCommunityMentionServiceServer(server, store)
		pb.RegisterCommunityReactionServiceServer(server, store)
		pb.RegisterCommunityFavoriteServiceServer(server, store)
		pb.RegisterPersonalNotificationServiceServer(server, store)
		pb.RegisterAuthServiceServer(server, postgres.NewAuth(store, serverID, origin))
		pb.RegisterDirectoryPairingServiceServer(server,store)
		pb.RegisterAssetServiceServer(server, store)
		pb.RegisterMlsServiceServer(server, store)
		pb.RegisterSyncServiceServer(server, store)
		pb.RegisterAdminServiceServer(server, store)
		pb.RegisterMembershipServiceServer(server, store)
	}
	return server
}

type homeHTTPConfig struct {
	remoteSetup                                                   bool
	output                                                        io.Writer
	address, localOrigin, publicOrigin, identityOrigin, setupCode string
	pendingSetupPath, statePath, data                             string
	certificate                                                   tls.Certificate
}

type publicHTTPServer struct {
	server    *http.Server
	listener  net.Listener
	statePath string
	witness   *home.NetworkWitness
	autoStop  func()
}

func (server *publicHTTPServer) close() {
	_ = server.server.Close()
	_ = server.listener.Close()
	if server.autoStop != nil {
		server.autoStop()
	}
	if server.witness != nil {
		server.witness.Close()
	}
	if server.statePath != "" {
		_ = os.Remove(server.statePath)
	}
}

func startPublicServer(ctx context.Context, config homeHTTPConfig, connection *grpc.ClientConn, store *postgres.Store, identity postgres.Identity, failures chan<- error) (*publicHTTPServer, error) {
	initialized := watchHomeSetup(ctx, store, config.pendingSetupPath)
	endpoint, _ := url.Parse(config.publicOrigin)
	handler, err := transport.HandlerWithEndpoint(ctx, connection, identity.ServerID, identity.PublicKey, endpoint.Host, true)
	if err != nil {
		return nil, err
	}
	handler = store.WithPush(transport.WithConnect(handler, connection, store))
	handler, err = bindHomeOrigin(ctx, handler, store, identity, config.identityOrigin, config.publicOrigin, config.certificate, config.data, true)
	if err != nil {
		return nil, err
	}
	if config.remoteSetup {
		handler = remoteSetupHandler(ctx, handler, initialized, store, config)
	} else {
		handler = requireHomeOwner(handler, initialized)
	}
	handler, witness, err := observeHomeNetwork(ctx, handler, config, identity.ServerID)
	if err != nil {
		return nil, err
	}

	server := &http.Server{
		Addr: config.address, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{config.certificate}},
	}
	listener, err := net.Listen("tcp", config.address)
	if err != nil {
		witness.Close()
		return nil, err
	}
	resource := &publicHTTPServer{server: server, listener: listener, witness: witness}
	go func() { failures <- server.ServeTLS(listener, "", "") }()
	var ready atomic.Bool
	baseConfig := config
	resource.autoStop = home.StartAutomaticNetwork(ctx, home.AutomaticNetworkOptions{Data: config.data, OnChanged: func() {
		network, err := home.ReadNetworkConfig(baseConfig.data)
		if err != nil || len(network.Endpoints) == 0 {
			return
		}
		witness.Update(ctx, network.AdvertisedEndpoints())
		if ready.Load() {
			_ = writeHomeState(baseConfig.statePath, baseConfig.localOrigin, network.Endpoints[0].Origin(), baseConfig.setupCode, identity.ServerID)
		}
	}})
	if network, err := home.ReadNetworkConfig(config.data); err == nil && len(network.Endpoints) > 0 {
		config.publicOrigin = network.Endpoints[0].Origin()
	}
	if err := writeHomeState(config.statePath, config.localOrigin, config.publicOrigin, config.setupCode, identity.ServerID); err != nil {
		resource.close()
		return nil, err
	}
	resource.statePath = config.statePath
	ready.Store(true)
	if config.remoteSetup && config.setupCode != "" {
		if err := printSetupLink(config, identity.ServerID); err != nil {
			resource.close()
			return nil, err
		}
	}
	return resource, nil
}

func requireHomeOwner(handler http.Handler, initialized *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !initialized.Load() {
			http.Error(response, "Сначала настройте владельца локально", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(response, request)
	})
}

func serverContext(managed bool) (context.Context, context.CancelFunc) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if managed {
		go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
	}
	return ctx, cancel
}

func storeVerifier(store *postgres.Store) authn.Verifier {
	var verifier authn.Verifier
	if store != nil {
		verifier = store
	}
	return verifier
}

type rpcBridge struct {
	listener   net.Listener
	server     *grpc.Server
	connection *grpc.ClientConn
}

func (bridge *rpcBridge) close() {
	bridge.connection.Close()
	bridge.server.Stop()
}
func startRPCBridge(ctx context.Context, options serverOptions, service *chat.Service, store *postgres.Store, identity postgres.Identity, origin, publicOrigin string, cancel context.CancelFunc) (*rpcBridge, error) {
	network, err := voice.NetworkFromEnvironment()
	if err != nil {
		return nil, err
	}
	voiceService, err := voice.NewService(ctx, store, network)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", options.grpcAddress)
	if err != nil {
		return nil, err
	}
	server := configuredRPCServer(service, store, identity.ServerID, authOrigin(origin, publicOrigin), voiceService, stopStreamsWith(ctx))
	go func() {
		if err := server.Serve(listener); err != nil {
			log.Printf("gRPC: %v", err)
			cancel()
		}
	}()
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		server.Stop()
		return nil, err
	}
	return &rpcBridge{listener, server, connection}, nil
}

func performPortableTransfer(options serverOptions) error {
	if options.homeRuntime == "" || options.backup != "" || options.restore != "" || options.restoreTo != "" || options.homeCheck || options.homeMigrate || options.homeStopped || options.runtimeInfo || options.checkDatabase || options.firstOwner || options.setupCode {
		return errors.New("Выберите только экспорт или импорт пространства и укажите runtime")
	}
	if options.portableExport != "" {
		if options.portableImport != "" || options.portableTo != "" || options.homeData == "" {
			return errors.New("Для экспорта нужен home-data")
		}
		return home.ExportPortable(options.homeRuntime, options.homeData, options.portableExport)
	}
	if options.portableImport == "" || options.portableTo == "" || options.homeData != "" {
		return errors.New("Для импорта нужны архив и новый каталог")
	}
	return home.ImportPortable(options.homeRuntime, options.portableImport, options.portableTo, options.publicIP)
}

func applyHomeNetwork(options *serverOptions) error {
	if options.homeData == "" {
		return nil
	}
	config, err := home.ReadNetworkConfig(options.homeData)
	if err != nil {
		return err
	}
	if len(config.Endpoints) > 0 && !options.httpsSpecified {
		options.httpsAddress = net.JoinHostPort(config.ListenHost, strconv.Itoa(config.ListenPort))
	}
	return nil
}
