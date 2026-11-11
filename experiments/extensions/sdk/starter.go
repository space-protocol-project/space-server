// Package sdk содержит воспроизводимый каркас серверного пакета P1.
// Генератор создаёт демонстрационный WASM, не переносит рабочие функции Space.
package sdk

import (
	"encoding/json"
	"errors"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/manifest"
)

const Version = "space.sdk.p1.1"

func Starter(plugin, title string, license []byte) (map[string][]byte, error) {
	if len(title) == 0 || len(title) > 128 || len(license) == 0 || len(license) > extensions.MaxJSON {
		return nil, errors.New("invalid starter")
	}
	m := manifest.Manifest{ManifestVersion: 1, PluginID: plugin, Version: "0.1.0", HostAPI: "space.plugin.p1", WasmABI: "space_plugin_p1", UIAPI: "space.ui.p1", Entrypoint: "server.wasm", DataSchema: 1, Permissions: []string{}, RequiredComponents: []string{"section.v1", "text.v1", "button.v1"}, Commands: map[string]string{"acknowledge": "schemas/empty.json"}, Queries: map[string]string{"overview": "schemas/empty.json"}}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if _, err = manifest.Decode(raw); err != nil {
		return nil, err
	}
	document, err := json.Marshal(map[string]any{
		"ui_api": "space.ui.p1",
		"strings": map[string]any{
			"ru": map[string]string{"title": title, "status": "Каркас расширения. Рабочие функции ещё не перенесены.", "action": "Проверить"},
			"en": map[string]string{"title": title, "status": "Extension scaffold. Application features are not migrated.", "action": "Check"},
		},
		"body": []any{map[string]any{"id": "main", "component": "section.v1", "label_key": "title", "children": []any{
			map[string]any{"id": "status", "component": "text.v1", "label_key": "status"},
			map[string]any{"id": "action", "component": "button.v1", "label_key": "action", "command": "acknowledge"},
		}}},
	})
	if err != nil {
		return nil, err
	}
	module, err := constantModule(document)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"manifest.json": raw, "server.wasm": module, "LICENSE": license, "schemas/empty.json": []byte(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)}, nil
}

func leb(value uint64) []byte {
	var result []byte
	for value >= 128 {
		result = append(result, byte(value)|128)
		value >>= 7
	}
	return append(result, byte(value))
}

func constant(value uint64) []byte {
	raw := leb(value)
	if raw[len(raw)-1]&64 != 0 {
		raw = append(raw[:len(raw)-1], raw[len(raw)-1]|128, 0)
	}
	return raw
}

func section(id byte, data []byte) []byte {
	result := append([]byte{id}, leb(uint64(len(data)))...)
	return append(result, data...)
}
func sized(data []byte) []byte                    { return append(leb(uint64(len(data))), data...) }
func export(name string, kind, index byte) []byte { return append(sized([]byte(name)), kind, index) }

func constantModule(document []byte) ([]byte, error) {
	if extensions.CheckJSON(document) != nil {
		return nil, errors.New("invalid document")
	}
	module := []byte{0, 97, 115, 109, 1, 0, 0, 0}
	module = append(module, section(1, []byte{3, 0x60, 1, 0x7f, 1, 0x7f, 0x60, 2, 0x7f, 0x7f, 0, 0x60, 2, 0x7f, 0x7f, 1, 0x7e})...)
	module = append(module, section(3, []byte{3, 0, 1, 2})...)
	module = append(module, section(5, []byte{1, 1, 2, 2})...)
	exports := []byte{4}
	for _, item := range []struct {
		name        string
		kind, index byte
	}{{"memory", 2, 0}, {"space_alloc", 0, 0}, {"space_free", 0, 1}, {"space_handle", 0, 2}} {
		exports = append(exports, export(item.name, item.kind, item.index)...)
	}
	module = append(module, section(7, exports)...)
	alloc := append([]byte{0, 0x41}, constant(1024)...)
	handle := append([]byte{0, 0x42}, constant(uint64(65536)<<32|uint64(len(document)))...)
	code := append([]byte{3}, sized(append(alloc, 0x0b))...)
	code = append(code, sized([]byte{0, 0x0b})...)
	code = append(code, sized(append(handle, 0x0b))...)
	module = append(module, section(10, code)...)
	data := append([]byte{1, 0, 0x41}, constant(65536)...)
	data = append(data, 0x0b)
	data = append(data, sized(document)...)
	return append(module, section(11, data)...), nil
}
