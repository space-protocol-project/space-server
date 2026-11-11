package demo

import (
	"bytes"
	"encoding/json"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/manifest"
	"github.com/space-protocol-project/space-server/experiments/extensions/payloadschema"
)

// FromManifest подключает полный manifest к ограниченному dispatcher P1.
// Неподдерживаемые декларации отклоняются, а не молча теряются при адаптации.
func FromManifest(raw, module []byte, schemas map[string][]byte) (Registration, error) {
	m, err := supportedManifest(raw)
	if err != nil {
		return Registration{}, err
	}
	expected := map[string]bool{}
	structured := false
	for _, path := range []string{m.Commands["acknowledge"], m.Queries["overview"]} {
		expected[path] = true
		s, err := payloadschema.Decode(schemas[path])
		if err != nil {
			return Registration{}, ErrRequest
		}
		structured = structured || len(s.Properties) != 0
	}
	if len(expected) != len(schemas) || len(module) == 0 || len(module) > extensions.MaxModule {
		return Registration{}, ErrRequest
	}
	r := Registration{Manifest: Manifest{HostAPI: m.HostAPI, ABI: m.WasmABI, UI: m.UIAPI, Contribution: "overview", Query: "overview", Command: "acknowledge"}, Module: bytes.Clone(module), SourceManifest: bytes.Clone(raw)}
	if structured {
		r.SourceSchemas = cloneSchemas(schemas)
	}
	return r, nil
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
	if r.SourceSchemas != nil {
		_, err = FromManifest(r.SourceManifest, r.Module, r.SourceSchemas)
		return err
	}
	return nil
}

func cloneSchemas(source map[string][]byte) map[string][]byte {
	if source == nil {
		return nil
	}
	result := map[string][]byte{}
	for name, raw := range source {
		result[name] = bytes.Clone(raw)
	}
	return result
}

func validatePayload(r Registration, request Request) error {
	if r.SourceSchemas == nil {
		if len(request.Payload) != 0 {
			return ErrRequest
		}
		return nil
	}
	m, err := supportedManifest(r.SourceManifest)
	if err != nil {
		return err
	}
	path := m.Queries[request.Name]
	if request.Kind == "command" {
		path = m.Commands[request.Name]
	}
	s, err := payloadschema.Decode(r.SourceSchemas[path])
	if err != nil {
		return ErrRequest
	}
	raw, err := json.Marshal(request.Payload)
	if err != nil || s.Validate(raw) != nil {
		return ErrRequest
	}
	return nil
}
