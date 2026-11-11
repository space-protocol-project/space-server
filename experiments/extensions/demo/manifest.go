package demo

import (
	"bytes"
	"encoding/json"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/manifest"
)

// FromManifest подключает полный manifest к ограниченному dispatcher P1.
// Неподдерживаемые декларации отклоняются, а не молча теряются при адаптации.
func FromManifest(raw, module []byte, schemas map[string][]byte) (Registration, error) {
	m, err := supportedManifest(raw)
	if err != nil {
		return Registration{}, err
	}
	expected := map[string]bool{}
	for _, path := range []string{m.Commands["acknowledge"], m.Queries["overview"]} {
		expected[path] = true
		if !emptyObjectSchema(schemas[path]) {
			return Registration{}, ErrRequest
		}
	}
	if len(expected) != len(schemas) || len(module) == 0 || len(module) > extensions.MaxModule {
		return Registration{}, ErrRequest
	}
	return Registration{Manifest: Manifest{HostAPI: m.HostAPI, ABI: m.WasmABI, UI: m.UIAPI, Contribution: "overview", Query: "overview", Command: "acknowledge"}, Module: bytes.Clone(module), SourceManifest: bytes.Clone(raw)}, nil
}

func supportedManifest(raw []byte) (manifest.Manifest, error) {
	m, err := manifest.Decode(raw)
	if err != nil {
		return m, ErrRequest
	}
	if m.DataSchema != 1 || len(m.Permissions) != 0 || len(m.Commands) != 1 || len(m.Queries) != 1 || m.Commands["acknowledge"] == "" || m.Queries["overview"] == "" {
		return m, ErrVersion
	}
	return m, nil
}

func checkSourceManifest(r Registration) error {
	m, err := supportedManifest(r.SourceManifest)
	if err != nil {
		return err
	}
	if m.HostAPI != r.Manifest.HostAPI || m.WasmABI != r.Manifest.ABI || m.UIAPI != r.Manifest.UI {
		return ErrVersion
	}
	return nil
}

func emptyObjectSchema(raw []byte) bool {
	if extensions.CheckJSON(raw) != nil {
		return false
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	canonical, err := json.Marshal(value)
	return err == nil && string(canonical) == `{"additionalProperties":false,"properties":{},"required":[],"type":"object"}`
}
