package sdk

import (
	"bytes"
	_ "embed"
	"encoding/json"

	"github.com/space-protocol-project/space-server/experiments/extensions/manifest"
)

//go:embed testdata/ui-form.json
var formDocument []byte

//go:embed testdata/form-payload.schema.json
var formSchema []byte

// FormStarter создаёт отдельный stateless-пример; рабочие данные он не изменяет.
func FormStarter() (map[string][]byte, error) {
	p, err := Starter("org.example.form", "Form", []byte("MIT"))
	if err != nil {
		return nil, err
	}
	m, err := manifest.Decode(p["manifest.json"])
	if err != nil {
		return nil, err
	}
	m.Commands["acknowledge"] = "schemas/form.json"
	m.RequiredComponents = []string{"form.v1", "input.v1", "toggle.v1", "choice.v1"}
	p["manifest.json"], err = json.Marshal(m)
	if err != nil {
		return nil, err
	}
	p["server.wasm"], err = constantModule(formDocument)
	if err != nil {
		return nil, err
	}
	p["schemas/form.json"] = bytes.Clone(formSchema)
	return p, nil
}
