package sdk

import (
	"bytes"
	"embed"
)

//go:embed testdata/manifest-v2.json testdata/contracts/*.json
var contractFiles embed.FS

// ContractStarter создаёт проверяемый WASM-пример manifest v2.
// Ответ фиксирован для стенда; это не рабочее расширение хранения заметок.
func ContractStarter(output []byte) (map[string][]byte, error) {
	module, err := constantModule(output)
	if err != nil {
		return nil, err
	}
	raw, err := contractFiles.ReadFile("testdata/manifest-v2.json")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{"manifest.json": bytes.Clone(raw), "server.wasm": module, "LICENSE": []byte("MIT")}
	for _, name := range []string{"empty", "result", "event"} {
		value, err := contractFiles.ReadFile("testdata/contracts/" + name + ".json")
		if err != nil {
			return nil, err
		}
		files["schemas/"+name+".json"] = bytes.Clone(value)
	}
	return files, nil
}
