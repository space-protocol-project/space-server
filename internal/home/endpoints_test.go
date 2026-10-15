package home

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEndpointOriginsAndValidation(t *testing.T) {
	for input, expected := range map[string]string{"https://space.example.org:9443": "https://space.example.org:9443", "https://[2001:db8::1]:443": "https://[2001:db8::1]"} {
		e, err := EndpointFromOrigin(input)
		if err != nil || e.Origin() != expected {
			t.Fatalf("%s: %v %s", input, err, e.Origin())
		}
	}
	for _, input := range []string{"https://user@host:443", "https://host/path", "https://host:65536", "https://host:0", "http://host", "https://host?query=1", "https://[fe80::1%25eth0]:443"} {
		if _, err := EndpointFromOrigin(input); err == nil {
			t.Fatal("Принят некорректный адрес", input)
		}
	}
}

func TestEndpointPayloadIsCanonical(t *testing.T) {
	endpoints := []Endpoint{{Host: "space.example.org", Port: 9443, Type: "https", Priority: 0}, {Host: "2001:db8::1", Port: 443, Type: "https", Priority: 1}}
	got, err := EndpointPayload("srv_test", "https://old:8443", 3, "pin", endpoints)
	want := "space/endpoints/v1\x00" + `["srv_test","https://old:8443",3,"pin",[["space.example.org",9443,"https",0],["2001:db8::1",443,"https",1]]]`
	if err != nil || string(got) != want {
		t.Fatalf("%s %v", got, err)
	}
}

func TestNetworkConfigSeparatesListeningAndExternalPort(t *testing.T) {
	root := t.TempDir()
	config := NetworkConfig{IdentityOrigin: "https://203.0.113.10:8443", Revision: 7, ListenHost: "::", ListenPort: 18443,
		Endpoints: []Endpoint{{Host: "space.example.org", Port: 443, Type: "https"}, {Host: "2001:db8::1", Port: 9443, Type: "https", Priority: 1}}}
	write := func() {
		t.Helper()
		content, _ := json.Marshal(config)
		if err := os.WriteFile(filepath.Join(root, "config.json"), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	loaded, err := ReadNetworkConfig(filepath.Join(root, "data"))
	if err != nil || loaded.ListenPort != 18443 || loaded.Endpoints[0].Origin() != "https://space.example.org" || loaded.Revision != 7 {
		t.Fatalf("%+v %v", loaded, err)
	}
	config.Endpoints = append(config.Endpoints, config.Endpoints[0])
	write()
	if _, err := ReadNetworkConfig(filepath.Join(root, "data")); err == nil {
		t.Fatal("Приняты повторные адреса")
	}
}

func TestPortableAddressPreservesBackupAndAdvancesRevision(t *testing.T) {
	root := t.TempDir()
	config := NetworkConfig{IP: "203.0.113.10", Port: 9443, IdentityOrigin: "https://203.0.113.10:8443", Revision: 7, ListenHost: "::", ListenPort: 18443,
		Endpoints: []Endpoint{{Host: "203.0.113.10", Port: 9443, Type: "https"}, {Host: "backup.example.org", Port: 443, Type: "https", Priority: 1}}}
	content, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(root, "config.json"), content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := setPortableAddress(root, "new.example.org"); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadNetworkConfig(filepath.Join(root, "data"))
	if err != nil || loaded.IdentityOrigin != config.IdentityOrigin || loaded.ListenPort != 18443 || loaded.Revision != 8 || loaded.Endpoints[0].Host != "new.example.org" || loaded.Endpoints[1] != config.Endpoints[1] {
		t.Fatalf("%+v %v", loaded, err)
	}
}
