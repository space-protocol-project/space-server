package packagep1_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/space-protocol-project/space-server/experiments/extensions/packagep1"
	"github.com/space-protocol-project/space-server/experiments/extensions/sdk"
)

func TestPackageContracts(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "missing_result", "remote_schema", "invalid_event", "extra_schema"} {
		t.Run(mode, func(t *testing.T) {
			files, err := sdk.ContractStarter([]byte(`{"ui_api":"space.ui.p1"}`))
			if err != nil {
				t.Fatal(err)
			}
			var m struct {
				Plugin string `json:"plugin_id"`
			}
			if json.Unmarshal(files["manifest.json"], &m) != nil {
				t.Fatal("fixture")
			}
			switch mode {
			case "missing_result":
				delete(files, "schemas/result.json")
			case "remote_schema":
				files["schemas/result.json"] = []byte(`{"$ref":"https://example.com/schema"}`)
			case "invalid_event":
				files["schemas/event.json"] = []byte(`{"type":"object"}`)
			case "extra_schema":
				files["schemas/extra.json"] = files["schemas/event.json"]
			}
			raw, err := packagep1.Pack(files, key)
			if err != nil {
				t.Fatal(err)
			}
			_, err = packagep1.Verify(raw, packagep1.Trust{Plugin: m.Plugin, Keys: map[string]ed25519.PublicKey{packagep1.Fingerprint(pub): pub}})
			if (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
		})
	}
}
