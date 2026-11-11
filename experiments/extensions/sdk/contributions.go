package sdk

import _ "embed"

//go:embed testdata/manifest-contributions.json
var contributionManifest []byte

// ContributionStarter — проверочный пример двух точек входа, не рабочее приложение.
func ContributionStarter() (map[string][]byte, error) {
	files, err := Starter("org.example.polls", "Contributions", []byte("MIT"))
	if err != nil {
		return nil, err
	}
	document := []byte(`{"ui_api":"space.ui.p1","strings":{"en":{"title":"Extension","action":"Refresh"},"ru":{"title":"Расширение","action":"Обновить"}},"body":[{"id":"main","component":"section.v1","label_key":"title","children":[{"id":"refresh","component":"button.v1","label_key":"action","command":"refresh"}]}]}`)
	module, err := constantModule(document)
	if err != nil {
		return nil, err
	}
	files["manifest.json"] = append([]byte(nil), contributionManifest...)
	files["server.wasm"] = module
	return files, nil
}
