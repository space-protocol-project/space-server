// Package manifest проверяет полный серверный manifest кандидата P1.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
)

var ErrManifest = errors.New("invalid_manifest")
var pluginID = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_-]*)+$`)
var versionID = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var operationID = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
var schemaPath = regexp.MustCompile(`^schemas/[a-z][a-z0-9_-]{0,63}\.json$`)

type Manifest struct {
	ManifestVersion    int               `json:"manifest_version"`
	PluginID           string            `json:"plugin_id"`
	Version            string            `json:"version"`
	HostAPI            string            `json:"host_api"`
	WasmABI            string            `json:"wasm_abi"`
	UIAPI              string            `json:"ui_api"`
	Entrypoint         string            `json:"entrypoint"`
	DataSchema         int64             `json:"data_schema"`
	Permissions        []string          `json:"permissions"`
	RequiredComponents []string          `json:"required_components"`
	Commands           map[string]string `json:"commands"`
	Queries            map[string]string `json:"queries"`
	Contributions      []Contribution    `json:"contributions,omitzero"`
	Contracts          *Contracts        `json:"contracts,omitempty"`
}

func Decode(raw []byte) (Manifest, error) {
	var m Manifest
	if extensions.CheckJSON(raw) != nil {
		return m, ErrManifest
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return m, ErrManifest
	}
	contributions, hasContributions := fields["contributions"]
	want := 12
	if hasContributions {
		want++
	}
	if contract, ok := fields["contracts"]; ok {
		want++
		if !contractsFields(contract) {
			return m, ErrManifest
		}
	}
	if len(fields) != want {
		return m, ErrManifest
	}
	if hasContributions && !contributionFields(contributions) {
		return m, ErrManifest
	}
	for _, name := range []string{"manifest_version", "plugin_id", "version", "host_api", "wasm_abi", "ui_api", "entrypoint", "data_schema", "permissions", "required_components", "commands", "queries"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return m, ErrManifest
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || !m.valid() {
		return Manifest{}, ErrManifest
	}
	if !validContributions(m.Contributions, m.Queries) || !m.validContracts() {
		return Manifest{}, ErrManifest
	}
	return m, nil
}

func (m Manifest) valid() bool {
	if (m.ManifestVersion != 1 && m.ManifestVersion != 2) || m.HostAPI != "space.plugin.p1" || m.WasmABI != "space_plugin_p1" || m.UIAPI != "space.ui.p1" || m.Entrypoint != "server.wasm" {
		return false
	}
	if len(m.PluginID) > 128 || !pluginID.MatchString(m.PluginID) || len(m.Version) > 32 || !versionID.MatchString(m.Version) || m.DataSchema < 1 || m.DataSchema > 2147483647 {
		return false
	}
	return unique(m.Permissions, 8, map[string]bool{"storage.own": true, "events.own": true}) && unique(m.RequiredComponents, 16, map[string]bool{"section.v1": true, "text.v1": true, "button.v1": true, "form.v1": true, "input.v1": true, "toggle.v1": true, "choice.v1": true}) && operations(m.Commands) && operations(m.Queries)
}

func unique(values []string, limit int, allowed map[string]bool) bool {
	if values == nil || len(values) > limit {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !allowed[value] || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func operations(values map[string]string) bool {
	if values == nil || len(values) > 32 {
		return false
	}
	for name, path := range values {
		if !operationID.MatchString(name) || !schemaPath.MatchString(path) {
			return false
		}
	}
	return true
}
