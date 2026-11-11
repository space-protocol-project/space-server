package manifest

import (
	"encoding/json"
	"os"
	"testing"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSharedManifest(t *testing.T) {
	m, err := Decode(fixture(t))
	if err != nil || m.PluginID != "org.example.polls" || m.Commands["vote"] != "schemas/vote.json" {
		t.Fatal(m, err)
	}
}

func TestManifestRequired(t *testing.T) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(fixture(t), &fields); err != nil {
		t.Fatal(err)
	}
	for name := range fields {
		for _, null := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				var changed map[string]any
				if err := json.Unmarshal(fixture(t), &changed); err != nil {
					t.Fatal(err)
				}
				if null {
					changed[name] = nil
				} else {
					delete(changed, name)
				}
				raw, err := json.Marshal(changed)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = Decode(raw); err == nil {
					t.Fatal("Принят неполный manifest")
				}
			})
		}
	}
}

func TestManifestRejects(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"version", "manifest_version", 2}, {"plugin", "plugin_id", "../plugin"},
		{"release", "version", "1.0"}, {"host", "host_api", "v99"}, {"abi", "wasm_abi", "v99"},
		{"ui", "ui_api", "v99"}, {"path", "entrypoint", "../server.wasm"},
		{"schema_zero", "data_schema", 0}, {"schema_fraction", "data_schema", 1.5}, {"schema_limit", "data_schema", int64(2147483648)},
		{"permission", "permissions", []string{"fs.read"}}, {"duplicate", "permissions", []string{"storage.own", "storage.own"}},
		{"component", "required_components", []string{"webview.v1"}},
		{"command", "commands", map[string]string{"../vote": "schemas/vote.json"}},
		{"query", "queries", map[string]string{"overview": "../schema.json"}},
		{"unknown", "extra", true}, {"case", "Host_API", "space.plugin.p1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fields map[string]any
			if err := json.Unmarshal(fixture(t), &fields); err != nil {
				t.Fatal(err)
			}
			fields[tc.field] = tc.value
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Decode(raw); err == nil {
				t.Fatal("Принят неверный manifest")
			}
		})
	}
}
