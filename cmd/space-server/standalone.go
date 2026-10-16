package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/space-protocol-project/space-server/internal/home"
)

func runStandalone(args []string, output io.Writer) error {
	options, err := standaloneOptions(args, output)
	if err != nil {
		return err
	}
	if err := applyHomeNetwork(&options); err != nil {
		return err
	}
	return runServer(options)
}

func standaloneOptions(args []string, output io.Writer) (serverOptions, error) {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	flags.SetOutput(output)
	directory := flags.String("data", "./space-home", "Каталог пространства (данные сохраняются при перезапуске)")
	address := flags.String("address", "", "Внешний HTTPS адрес, например https://space.example.org:8443; иначе IP определяется через ipify")
	listen := flags.String("listen", "0.0.0.0:8443", "Адрес прослушивания HTTPS")
	managed := flags.Bool("managed", false, "Завершаться при закрытии stdin управляющего процесса")
	if err := flags.Parse(args); err != nil {
		return serverOptions{}, err
	}
	if flags.NArg() != 0 {
		return serverOptions{}, fmt.Errorf("Неизвестные аргументы start")
	}
	executable, err := os.Executable()
	if err != nil {
		return serverOptions{}, err
	}
	data, err := filepath.Abs(filepath.Join(*directory, "data"))
	if err != nil {
		return serverOptions{}, err
	}
	options := serverOptions{homeRuntime: filepath.Dir(executable), homeData: data, port: "127.0.0.1:0", grpcAddress: "127.0.0.1:0", httpsAddress: *listen, remoteSetup: true, output: output}
	options.managed = *managed
	if err := standaloneNetwork(data, *address, *listen); err != nil {
		return serverOptions{}, err
	}
	return options, nil
}

func standaloneNetwork(data, address, listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || net.ParseIP(host) == nil || port == "0" {
		return fmt.Errorf("Нужен IP и ненулевой порт прослушивания")
	}
	var listenPort int
	if _, err := fmt.Sscan(port, &listenPort); err != nil || listenPort < 1 || listenPort > 65535 {
		return fmt.Errorf("Некорректный порт")
	}
	config, err := home.ReadNetworkConfig(data)
	if err != nil {
		return err
	}
	if len(config.Endpoints) > 0 {
		if address != "" && address != config.Endpoints[0].Origin() {
			return fmt.Errorf("Адрес уже сохранён; измените config.json при остановленном сервере")
		}
		return nil
	}
	if address == "" {
		ip, err := standalonePublicIP()
		if err != nil {
			return err
		}
		address = "https://" + net.JoinHostPort(ip, port)
	}
	endpoint, err := home.EndpointFromOrigin(address)
	if err != nil {
		return err
	}
	config = home.NetworkConfig{Endpoints: []home.Endpoint{endpoint}, Revision: 1, ListenHost: host, ListenPort: listenPort}
	return writeStandaloneNetwork(data, config)
}

func writeStandaloneNetwork(data string, config home.NetworkConfig) error {
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(data), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(filepath.Dir(data), "config.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(encoded)
	return err
}

func standalonePublicIP() (string, error) {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get("https://api.ipify.org")
	if err != nil {
		return "", fmt.Errorf("Не удалось определить IP; укажите -address https://АДРЕС:ПОРТ")
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 128))
	ip := net.ParseIP(strings.TrimSpace(string(content)))
	if err != nil || response.StatusCode != 200 || ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return "", fmt.Errorf("Не удалось определить IP; укажите -address https://АДРЕС:ПОРТ")
	}
	return ip.String(), nil
}
