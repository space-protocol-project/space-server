package manifest

import (
	"encoding/json"
	"os"
	"testing"
)

func TestManifestV2Contracts(t *testing.T) {
	raw, err := os.ReadFile("testdata/manifest-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Decode(raw)
	if err != nil || m.Contracts.Commands["save"].Effect != "mutation" {
		t.Fatal(m, err)
	}
	for name, change := range map[string]func(*Manifest){
		"missing_contracts":     func(m *Manifest) { m.Contracts = nil },
		"missing_operation":     func(m *Manifest) { delete(m.Contracts.Commands, "save") },
		"extra_operation":       func(m *Manifest) { m.Contracts.Commands["other"] = m.Contracts.Commands["save"] },
		"undeclared_capability": func(m *Manifest) { m.Permissions = []string{"storage.own"} },
		"duplicate_capability": func(m *Manifest) {
			o := m.Contracts.Commands["save"]
			o.Capabilities = []string{"storage.own", "storage.own"}
			m.Contracts.Commands["save"] = o
		},
		"missing_storage": func(m *Manifest) {
			o := m.Contracts.Commands["save"]
			o.Capabilities = []string{"events.own"}
			m.Contracts.Commands["save"] = o
		},
		"missing_events_grant": func(m *Manifest) {
			o := m.Contracts.Commands["save"]
			o.Capabilities = []string{"storage.own"}
			m.Contracts.Commands["save"] = o
		},
		"query_mutation": func(m *Manifest) { m.Contracts.Queries["overview"] = m.Contracts.Commands["save"] },
		"unknown_event":  func(m *Manifest) { delete(m.Contracts.Events, "note_changed") },
		"unsafe_result": func(m *Manifest) {
			o := m.Contracts.Commands["save"]
			o.Result = "../result.json"
			m.Contracts.Commands["save"] = o
		},
		"legacy_version": func(m *Manifest) { m.ManifestVersion = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			var v Manifest
			if json.Unmarshal(raw, &v) != nil {
				t.Fatal("fixture")
			}
			change(&v)
			value, _ := json.Marshal(v)
			if _, err := Decode(value); err == nil {
				t.Fatal("Принят неверный контракт")
			}
		})
	}
}
