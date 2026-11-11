package manifest

import (
	"encoding/json"
	"os"
	"testing"
)

func contributionFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/manifest-contributions.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestContributionsManifest(t *testing.T) {
	m, err := Decode(contributionFixture(t))
	if err != nil || len(m.Contributions) != 2 {
		t.Fatal(m, err)
	}
	m.Contributions = []Contribution{}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	m, err = Decode(raw)
	if err != nil || m.Contributions == nil || len(m.Contributions) != 0 {
		t.Fatal("Пустой реестр потерян", m, err)
	}
}

func TestContributionsReject(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any, []any)
	}{
		{"duplicate", func(m map[string]any, a []any) { m["contributions"] = append(a, a[0]) }},
		{"limit", func(m map[string]any, a []any) {
			for len(a) < 33 {
				a = append(a, a[0])
			}
			m["contributions"] = a
		}},
		{"null", func(m map[string]any, _ []any) { m["contributions"] = nil }},
		{"query", func(_ map[string]any, a []any) { a[0].(map[string]any)["query"] = "missing" }},
		{"command", func(_ map[string]any, a []any) { a[0].(map[string]any)["query"] = "refresh" }},
		{"kind", func(_ map[string]any, a []any) { a[0].(map[string]any)["kind"] = "webview" }},
		{"case", func(_ map[string]any, a []any) { c := a[0].(map[string]any); c["Kind"] = c["kind"]; delete(c, "kind") }},
		{"extra", func(_ map[string]any, a []any) { a[0].(map[string]any)["url"] = "https://example.com" }},
		{"fallback", func(_ map[string]any, a []any) {
			a[0].(map[string]any)["title"] = map[string]string{"ru": "Заголовок"}
		}},
		{"blank", func(_ map[string]any, a []any) { a[0].(map[string]any)["title"] = map[string]string{"en": "  "} }},
		{"control", func(_ map[string]any, a []any) {
			a[0].(map[string]any)["title"] = map[string]string{"en": "line\nnext"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal(contributionFixture(t), &m); err != nil {
				t.Fatal(err)
			}
			test.change(m, m["contributions"].([]any))
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Decode(raw); err == nil {
				t.Fatal("Принята неверная декларация")
			}
		})
	}
}
