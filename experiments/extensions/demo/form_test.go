package demo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/space-protocol-project/space-server/experiments/extensions/sdk"
)

func TestLegacyPayloadRejectedBeforeWASM(t *testing.T) {
	h := &fakeHost{}
	d := setup(t, h, grant)
	r, err := Decode([]byte(`{"protocol":"space.dispatch.p1","instance":"demo","kind":"query","name":"overview","payload":{"principal_id":"admin"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Dispatch(context.Background(), "", r); !errors.Is(err, ErrRequest) || h.calls != 0 {
		t.Fatal(err, h.calls)
	}
}

func TestFormPayloadBeforeWASM(t *testing.T) {
	p, err := sdk.Starter("org.example.form", "Form", []byte("MIT"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(p["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	m["commands"] = map[string]string{"acknowledge": "schemas/form.json"}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../payloadschema/testdata/form-payload.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := FromManifest(raw, p["server.wasm"], map[string][]byte{"schemas/empty.json": p["schemas/empty.json"], "schemas/form.json": schema})
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHost{}
	d, err := New(h, grant, map[string]Registration{"demo": r})
	if err != nil {
		t.Fatal(err)
	}
	// Снимок регистрации не зависит от последующих изменений исходных схем.
	r.SourceSchemas["schemas/form.json"][0] = '!'
	q := request()
	q.Kind = "command"
	q.Name = "acknowledge"
	if _, err = d.Dispatch(context.Background(), "", q); !errors.Is(err, ErrRequest) || h.calls != 0 {
		t.Fatal(err, h.calls)
	}
	q.Payload = map[string]json.RawMessage{"message": json.RawMessage(`"ok"`), "kind": json.RawMessage(`"a"`), "enabled": json.RawMessage(`true`)}
	if _, err = d.Dispatch(context.Background(), "", q); err != nil || h.calls != 1 {
		t.Fatal(err, h.calls)
	}
	q.Payload["principal_id"] = json.RawMessage(`"admin"`)
	if _, err = d.Dispatch(context.Background(), "", q); !errors.Is(err, ErrRequest) || h.calls != 1 {
		t.Fatal(err, h.calls)
	}
}
