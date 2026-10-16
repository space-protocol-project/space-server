package home

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/space-protocol-project/space-server/internal/portmap"
)

type AutomaticNetworkStatus struct {
	Format    int    `json:"format"`
	Code      string `json:"code"`
	Protocol  string `json:"protocol,omitempty"`
	External  string `json:"external,omitempty"`
	ExpiresAt int64  `json:"expires_at_unix_ms,omitempty"`
	IPCode    string `json:"ip_code,omitempty"`
	At        int64  `json:"at_unix_ms"`
}

type AutomaticNetworkOptions struct {
	Data      string
	Route     func() (portmap.Route, error)
	Map       func(context.Context, portmap.Route, uint16, uint16) (*portmap.Mapping, error)
	PublicIP  func(context.Context) (netip.Addr, error)
	Interval  time.Duration
	OnChanged func()
}

type automaticNetwork struct {
	options                   AutomaticNetworkOptions
	mapping                   *portmap.Mapping
	route                     portmap.Route
	renewed, expires, checked time.Time
	status                    AutomaticNetworkStatus
}

func StartAutomaticNetwork(ctx context.Context, options AutomaticNetworkOptions) func() {
	if options.Route == nil {
		options.Route = portmap.DefaultRoute
	}
	if options.Map == nil {
		options.Map = portmap.Create
	}
	if options.PublicIP == nil {
		options.PublicIP = DetectPublicIPv4
	}
	if options.Interval == 0 {
		options.Interval = 15 * time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	manager := &automaticNetwork{options: options}
	manager.step(ctx, time.Now())
	go func() {
		defer close(done)
		defer manager.close()
		ticker := time.NewTicker(options.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				manager.step(ctx, time.Now())
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (m *automaticNetwork) close() {
	if m.mapping != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = m.mapping.Delete(ctx)
		m.mapping = nil
	}
	_ = os.Remove(filepath.Join(m.options.Data, "automatic-network.json"))
}

func (m *automaticNetwork) step(parent context.Context, now time.Time) {
	config, err := ReadNetworkConfig(m.options.Data)
	if err != nil || len(config.Endpoints) == 0 {
		return
	}
	lastIPCode := m.status.IPCode
	m.status = AutomaticNetworkStatus{Format: 1, Code: "disabled", At: now.UnixMilli()}
	if config.AutoPublicIP {
		m.status.IPCode = lastIPCode
	}
	if !config.AutoPortMapping && m.mapping != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = m.mapping.Delete(ctx)
		cancel()
		m.mapping = nil
	}
	if config.AutoPortMapping {
		m.mapPort(parent, config, now)
	}
	if config.AutoPublicIP {
		m.updateIP(parent, now)
	}
	_ = writeNetworkJSON(filepath.Join(m.options.Data, "automatic-network.json"), m.status)
}

func (m *automaticNetwork) mapPort(parent context.Context, config NetworkConfig, now time.Time) {
	m.status.Code = "pending"
	route, err := m.options.Route()
	if err != nil {
		m.status.Code = "no_gateway"
		return
	}
	ctx, cancel := context.WithTimeout(parent, 13*time.Second)
	defer cancel()
	if m.mapping != nil && route != m.route {
		_ = m.mapping.Delete(ctx)
		m.mapping = nil
	}
	refreshed := false
	var next *portmap.Mapping
	if m.mapping == nil {
		refreshed = true
		next, err = m.options.Map(ctx, route, uint16(config.ListenPort), uint16(config.Endpoints[0].Port))
	} else if !now.Before(m.renewed.Add(m.mapping.Lifetime / 2)) {
		refreshed = true
		next, err = m.mapping.Renew(ctx)
	} else {
		next = m.mapping
	}
	if err != nil {
		m.status.Code = "mapping_failed"
		if m.mapping != nil {
			_ = m.mapping.Delete(ctx)
			m.mapping = nil
			m.status.Code = "renewal_failed"
		}
		return
	}
	if next == nil {
		return
	}
	if refreshed {
		m.renewed = now
		m.expires = now.Add(next.Lifetime)
	}
	m.mapping = next
	m.route = route
	m.showMapping()
	if changed, updateErr := UpdateAutomaticEndpoint(m.options.Data, next.External, config.AutoPublicIP); updateErr != nil {
		m.status.Code = "configuration_failed"
	} else if changed && m.options.OnChanged != nil {
		m.options.OnChanged()
	}
}

func (m *automaticNetwork) showMapping() {
	m.status.Code = "mapped"
	m.status.Protocol = m.mapping.Protocol
	m.status.External = m.mapping.External.String()
	m.status.ExpiresAt = m.expires.UnixMilli()
}

func (m *automaticNetwork) updateIP(parent context.Context, now time.Time) {
	if m.mapping != nil && now.Before(m.expires) {
		m.status.IPCode = "router"
		return
	}
	if !m.checked.IsZero() && now.Sub(m.checked) < time.Minute {
		return
	}
	m.checked = now
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	ip, err := m.options.PublicIP(ctx)
	if err != nil || !portmap.PublicIP(ip) || !ip.Is4() {
		m.status.IPCode = "unavailable"
		return
	}
	config, err := ReadNetworkConfig(m.options.Data)
	if err != nil {
		m.status.IPCode = "configuration_failed"
		return
	}
	changed, err := UpdateAutomaticEndpoint(m.options.Data, netip.AddrPortFrom(ip, uint16(config.Endpoints[0].Port)), true)
	m.status.IPCode = "updated"
	if err != nil {
		m.status.IPCode = "configuration_failed"
	}
	if changed && m.options.OnChanged != nil {
		m.options.OnChanged()
	}
}

func DetectPublicIPv4(ctx context.Context) (netip.Addr, error) {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	response, err := client.Do(request)
	if err != nil {
		return netip.Addr{}, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 129))
	if err != nil || len(content) > 128 || response.StatusCode != 200 {
		return netip.Addr{}, fmt.Errorf("Не удалось определить публичный IP")
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(content)))
	if err != nil || !ip.Is4() || !portmap.PublicIP(ip) {
		return netip.Addr{}, fmt.Errorf("IP не является публичным IPv4")
	}
	return ip, nil
}

func UpdateAutomaticEndpoint(data string, address netip.AddrPort, host bool) (bool, error) {
	if !address.Addr().Is4() || !portmap.PublicIP(address.Addr()) || address.Port() == 0 {
		return false, fmt.Errorf("Непубличный автоматический адрес")
	}
	config, err := ReadNetworkConfig(data)
	if err != nil {
		return false, err
	}
	if len(config.Endpoints) == 0 {
		return false, fmt.Errorf("Нет адресов пространства")
	}
	previous := config.Endpoints[0]
	if config.IdentityOrigin == "" {
		config.IdentityOrigin = previous.Origin()
	}
	if host && config.AutoPublicIP {
		config.Endpoints[0].Host = address.Addr().String()
	}
	if config.AutoPortMapping {
		config.Endpoints[0].Port = int(address.Port())
	}
	if previous == config.Endpoints[0] {
		return false, nil
	}
	config.PreviousAutomatic = &previous
	if config.Revision >= MaxEndpointRevision {
		return false, fmt.Errorf("Исчерпана версия списка адресов")
	}
	config.Revision++
	config.IP = config.Endpoints[0].Host
	config.Port = config.Endpoints[0].Port
	if err = ValidateEndpoints(config.Endpoints); err != nil {
		return false, err
	}
	path := filepath.Join(filepath.Dir(data), "config.json")
	// Сохраняем неизвестные поля конфигурации и постоянную identity_origin.
	content, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(content, &fields); err != nil {
		return false, err
	}
	encoded, _ := json.Marshal(config)
	var updates map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &updates)
	for key, value := range updates {
		fields[key] = value
	}
	return true, writeNetworkJSON(path, fields)
}

func writeNetworkJSON(path string, value any) error {
	content, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".network-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	if closed := file.Close(); err == nil {
		err = closed
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
