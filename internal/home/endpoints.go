package home

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const MaxEndpointRevision int64 = 9007199254740991

type Endpoint struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Type     string `json:"type"`
	Priority int    `json:"priority"`
}

func (e Endpoint) Origin() string {
	if e.Port == 443 {
		host := e.Host
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		return "https://" + host
	}
	return "https://" + net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

var domainLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func ValidEndpointHost(host string) bool {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "%[]") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if !domainLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func ValidateEndpoints(endpoints []Endpoint) error {
	if len(endpoints) == 0 || len(endpoints) > 8 {
		return fmt.Errorf("Нужно от 1 до 8 адресов")
	}
	seen := map[string]bool{}
	for _, e := range endpoints {
		if !ValidEndpointHost(e.Host) || e.Host != strings.ToLower(e.Host) || e.Port < 1 || e.Port > 65535 || e.Type != "https" || e.Priority < 0 || e.Priority > 255 || seen[e.Origin()] {
			return fmt.Errorf("Некорректный или повторный адрес пространства")
		}
		seen[e.Origin()] = true
	}
	return nil
}

type NetworkConfig struct {
	IP             string     `json:"ip"`
	Port           int        `json:"port"`
	IdentityOrigin string     `json:"identity_origin"`
	Endpoints      []Endpoint `json:"endpoints"`
	Revision       int64      `json:"endpoint_revision"`
	ListenHost     string     `json:"listen_host"`
	ListenPort     int        `json:"listen_port"`
}

func ReadNetworkConfig(data string) (NetworkConfig, error) {
	var config NetworkConfig
	content, err := os.ReadFile(filepath.Join(filepath.Dir(data), "config.json"))
	if os.IsNotExist(err) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	if len(content) > 16384 {
		return config, fmt.Errorf("Слишком большая конфигурация адресов")
	}
	err = json.Unmarshal(content, &config)
	if err != nil || len(config.Endpoints) == 0 {
		return config, err
	}
	if config.Revision < 1 || config.Revision > MaxEndpointRevision || config.ListenPort < 1 || config.ListenPort > 65535 || net.ParseIP(config.ListenHost) == nil {
		return config, fmt.Errorf("Некорректная версия или адрес прослушивания")
	}
	return config, ValidateEndpoints(config.Endpoints)
}

func EndpointFromOrigin(origin string) (Endpoint, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return Endpoint{}, err
	}
	port := 443
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
	}
	e := Endpoint{Host: strings.ToLower(u.Hostname()), Port: port, Type: "https"}
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return e, fmt.Errorf("Некорректный HTTPS адрес")
	}
	return e, ValidateEndpoints([]Endpoint{e})
}

// Массивы задают одинаковое каноническое представление в Go и Dart.
func EndpointPayload(serverID, identity string, revision int64, fingerprint string, endpoints []Endpoint) ([]byte, error) {
	rows := make([][]any, len(endpoints))
	for i, e := range endpoints {
		rows[i] = []any{e.Host, e.Port, e.Type, e.Priority}
	}
	payload, err := json.Marshal([]any{serverID, identity, revision, fingerprint, rows})
	return append([]byte("space/endpoints/v1\x00"), payload...), err
}
