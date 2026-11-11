package demo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/space-protocol-project/space-server/experiments/extensions/sdk"
)

func TestFullManifestDispatcher(t *testing.T) {
	p, err := sdk.Starter("org.example.starter", "Пример", []byte("MIT"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := FromManifest(p["manifest.json"], p["server.wasm"], map[string][]byte{"schemas/empty.json": p["schemas/empty.json"]})
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHost{}
	d, err := New(h, grant, map[string]Registration{"demo": r})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Dispatch(context.Background(), "", request()); err != nil || h.calls != 1 {
		t.Fatal(err, h.calls)
	}
	r.SourceManifest[0] = '!'
	if _, err = d.Dispatch(context.Background(), "", request()); err != nil {
		t.Fatal("Изменение источника повлияло на снимок", err)
	}
}

func TestFullManifestUnsupported(t *testing.T) {
	for _, mode := range []string{"permission", "command", "schema", "missing_schema", "schema_body"} {
		t.Run(mode, func(t *testing.T) {
			p, err := sdk.Starter("org.example.starter", "Пример", []byte("MIT"))
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err = json.Unmarshal(p["manifest.json"], &m); err != nil {
				t.Fatal(err)
			}
			schemas := map[string][]byte{"schemas/empty.json": p["schemas/empty.json"]}
			switch mode {
			case "permission":
				m["permissions"] = []string{"storage.own"}
			case "command":
				m["commands"] = map[string]string{"vote": "schemas/missing.json"}
			case "schema":
				m["data_schema"] = 2
			case "missing_schema":
				delete(schemas, "schemas/empty.json")
			case "schema_body":
				schemas["schemas/empty.json"] = []byte(`{"type":"object"}`)
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = FromManifest(raw, p["server.wasm"], schemas); err == nil {
				t.Fatal("Неподдерживаемый контракт принят")
			}
		})
	}
}
