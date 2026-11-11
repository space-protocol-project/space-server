package demo

import (
	"context"
	"testing"

	"github.com/space-protocol-project/space-server/experiments/extensions/sdk"
)

func TestContractResult(t *testing.T) {
	files, err := sdk.ContractStarter([]byte(`{"ui_api":"space.ui.p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string][]byte{}
	for _, name := range []string{"empty", "result", "event"} {
		schemas["schemas/"+name+".json"] = files["schemas/"+name+".json"]
	}
	r, err := FromManifest(files["manifest.json"], files["server.wasm"], schemas)
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(&fakeHost{}, grant, map[string]Registration{"demo": r})
	if err != nil {
		t.Fatal(err)
	}
	if !d.IsMutation("demo", "save") || d.IsMutation("demo", "unknown") {
		t.Fatal("Неверный эффект команды")
	}
	if _, err = d.Dispatch(context.Background(), "", request()); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"ui_api":"space.ui.p1","extra":true}`, `{"ui_api":true}`, `{}`} {
		if validateResult(r, request(), []byte(raw)) == nil {
			t.Fatal("Некорректный результат принят")
		}
	}
	r.SourceSchemas["schemas/result.json"][0] = '!'
	if _, err = d.Dispatch(context.Background(), "", request()); err != nil {
		t.Fatal("Изменился снимок схемы", err)
	}
}
