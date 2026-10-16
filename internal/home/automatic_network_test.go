package home

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/space-protocol-project/space-server/internal/portmap"
)

func automaticFixture(t *testing.T, host string) (string, NetworkConfig) {
	t.Helper()
	root := t.TempDir()
	// Windows может завершать удаление переименованных файлов асинхронно.
	// Повторяем очистку только собственного тестового каталога, не скрывая ошибку.
	t.Cleanup(func() {
		var err error
		for attempt := 0; attempt < 20; attempt++ {
			if err = os.RemoveAll(root); err == nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Errorf("Не удалось очистить тестовый каталог: %v", err)
	})
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	config := NetworkConfig{Endpoints: []Endpoint{{Host: host, Port: 8443, Type: "https"}, {Host: "backup.example.org", Port: 443, Type: "https", Priority: 1}}, Revision: 3, ListenHost: "::", ListenPort: 18443, AutoPortMapping: true, AutoPublicIP: host == "8.8.4.4"}
	if err := writeNetworkJSON(filepath.Join(root, "config.json"), config); err != nil {
		t.Fatal(err)
	}
	return data, config
}

func TestAutomaticEndpointPreservesIdentityAndManualDomain(t *testing.T) {
	for _, host := range []string{"8.8.4.4", "space.example.org"} {
		t.Run(host, func(t *testing.T) {
			data, config := automaticFixture(t, host)
			changed, err := UpdateAutomaticEndpoint(data, netip.MustParseAddrPort("8.8.8.8:9443"), true)
			if err != nil || !changed {
				t.Fatal(changed, err)
			}
			next, err := ReadNetworkConfig(data)
			if err != nil {
				t.Fatal(err)
			}
			expected := host
			if config.AutoPublicIP {
				expected = "8.8.8.8"
			}
			if next.Endpoints[0].Host != expected || next.IdentityOrigin != config.Endpoints[0].Origin() || next.ListenPort != 18443 || next.Revision != 4 || next.Endpoints[1] != config.Endpoints[1] {
				t.Fatalf("потеря конфигурации: %+v", next)
			}
			if len(next.AdvertisedEndpoints()) != 3 || next.PreviousAutomatic.Origin() != config.Endpoints[0].Origin() {
				t.Fatal("нет прежнего маршрута")
			}
			changed, err = UpdateAutomaticEndpoint(data, netip.MustParseAddrPort("8.8.8.8:9443"), true)
			if err != nil || changed {
				t.Fatal("лишнее обновление версии", err)
			}
		})
	}
}

func TestAutomaticLeaseRenewalAndCleanup(t *testing.T) {
	data, _ := automaticFixture(t, "8.8.4.4")
	creates, renews, deletes, changes := 0, 0, 0, 0
	var mapping *portmap.Mapping
	mapping = &portmap.Mapping{External: netip.MustParseAddrPort("8.8.8.8:9443"), Protocol: "PCP", Lifetime: 60 * time.Second,
		Renew: func(context.Context) (*portmap.Mapping, error) { renews++; return mapping, nil }, Delete: func(context.Context) error { deletes++; return nil }}
	manager := automaticNetwork{options: AutomaticNetworkOptions{Data: data, Route: func() (portmap.Route, error) { return portmap.Route{}, nil }, Map: func(_ context.Context, _ portmap.Route, internal, external uint16) (*portmap.Mapping, error) {
		creates++
		if internal != 18443 || external != 8443 {
			t.Fatal(internal, external)
		}
		return mapping, nil
	}, OnChanged: func() { changes++ }}}
	now := time.Now()
	manager.step(context.Background(), now)
	manager.step(context.Background(), now.Add(31*time.Second))
	manager.step(context.Background(), now.Add(40*time.Second))
	if creates != 1 || renews != 1 || changes != 1 || !manager.expires.Equal(now.Add(91*time.Second)) {
		t.Fatal(creates, renews, changes, manager.expires)
	}
	manager.close()
	if deletes != 1 {
		t.Fatal("аренда не удалена")
	}
	if _, err := os.Stat(filepath.Join(data, "automatic-network.json")); !os.IsNotExist(err) {
		t.Fatal("статус не очищен")
	}
}

func TestAutomaticIPBudgetAndUnknownFields(t *testing.T) {
	data, config := automaticFixture(t, "8.8.4.4")
	config.AutoPortMapping = false
	content, _ := json.Marshal(config)
	var raw map[string]any
	_ = json.Unmarshal(content, &raw)
	raw["custom"] = true
	_ = writeNetworkJSON(filepath.Join(filepath.Dir(data), "config.json"), raw)
	calls := 0
	manager := automaticNetwork{options: AutomaticNetworkOptions{Data: data, PublicIP: func(context.Context) (netip.Addr, error) { calls++; return netip.MustParseAddr("8.8.8.8"), nil }}}
	now := time.Now()
	manager.step(context.Background(), now)
	manager.step(context.Background(), now.Add(15*time.Second))
	manager.step(context.Background(), now.Add(61*time.Second))
	if calls != 2 {
		t.Fatal(calls)
	}
	content, _ = os.ReadFile(filepath.Join(filepath.Dir(data), "config.json"))
	_ = json.Unmarshal(content, &raw)
	if raw["custom"] != true {
		t.Fatal("неизвестные настройки потеряны")
	}
	next, _ := ReadNetworkConfig(data)
	if next.Revision != 4 {
		t.Fatal("неизменный IP меняет версию")
	}
}

func TestMappingNeverProvesExternalReachability(t *testing.T) {
	data, _ := automaticFixture(t, "8.8.4.4")
	now := time.Now()
	status := AutomaticNetworkStatus{Format: 1, Code: "mapped", Protocol: "PCP", External: "8.8.8.8:9443", At: now.UnixMilli(), ExpiresAt: now.Add(time.Minute).UnixMilli()}
	_ = writeNetworkJSON(filepath.Join(data, "automatic-network.json"), status)
	report := DoctorReport{External: "unverified"}
	appendAutomaticChecks(&report, data, now)
	if len(report.Checks) != 1 || report.External != "unverified" {
		t.Fatal(report)
	}
	report.Checks = nil
	status.ExpiresAt = now.Add(time.Hour).UnixMilli()
	_ = writeNetworkJSON(filepath.Join(data, "automatic-network.json"), status)
	appendAutomaticChecks(&report, data, now)
	if len(report.Checks) != 0 {
		t.Fatal("принят чрезмерный срок аренды")
	}
	status.ExpiresAt = now.Add(time.Minute).UnixMilli()
	status.External = "100.64.0.1:9443"
	_ = writeNetworkJSON(filepath.Join(data, "automatic-network.json"), status)
	appendAutomaticChecks(&report, data, now)
	if len(report.Checks) != 0 {
		t.Fatal("принят CGNAT в статусе аренды")
	}
	status.External = "8.8.8.8:9443"
	_ = writeNetworkJSON(filepath.Join(data, "automatic-network.json"), status)
	appendAutomaticChecks(&report, data, now.Add(time.Minute))
	if len(report.Checks) != 0 {
		t.Fatal("принята устаревшая аренда")
	}
}

func TestFailedRenewalDoesNotAdvertiseUnconfirmedMapping(t *testing.T) {
	data, _ := automaticFixture(t, "space.example.org")
	deleted := 0
	mapping := &portmap.Mapping{External: netip.MustParseAddrPort("8.8.8.8:9443"), Protocol: "PCP", Lifetime: 60 * time.Second,
		Renew: func(context.Context) (*portmap.Mapping, error) {
			return nil, errors.New("шлюз недоступен")
		}, Delete: func(context.Context) error { deleted++; return nil }}
	manager := automaticNetwork{options: AutomaticNetworkOptions{Data: data, Route: func() (portmap.Route, error) { return portmap.Route{}, nil }, Map: func(context.Context, portmap.Route, uint16, uint16) (*portmap.Mapping, error) { return mapping, nil }}}
	now := time.Now()
	manager.step(context.Background(), now)
	manager.step(context.Background(), now.Add(31*time.Second))
	if manager.mapping != nil || deleted != 1 || manager.status.Code != "renewal_failed" || manager.status.External != "" {
		t.Fatal(manager.status, deleted)
	}
	config, _ := ReadNetworkConfig(data)
	if config.Revision != 4 {
		t.Fatal("сбой продления изменил адрес")
	}
}
