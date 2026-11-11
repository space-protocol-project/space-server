package postgres

import (
	"context"
	"testing"

	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	"github.com/space-protocol-project/space-server/experiments/extensions/sdk"
	"google.golang.org/grpc/codes"
)

func TestExtensionP1FullManifest(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "manifest_test")
	p, err := sdk.Starter("org.example.starter", "Пример", []byte("MIT"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := demo.FromManifest(p["manifest.json"], p["server.wasm"], map[string][]byte{"schemas/empty.json": p["schemas/empty.json"]})
	if err != nil {
		t.Fatal(err)
	}
	b := map[string]ExtensionP1MutationBinding{"demo": {Installation: "install", Channel: c.Id, Registration: r}}
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return []byte(`{"ui_api":"space.ui.p1"}`), nil }}
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, b)
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	api := extensionP1RPCClient(t, f, s)
	if _, err = api.QueryExtension(f.owner, extensionP1Query()); err != nil {
		t.Fatal(err)
	}
	if _, err = api.ExecuteExtensionCommand(f.owner, extensionP1Command()); err != nil {
		t.Fatal(err)
	}
	_, err = api.ExecuteExtensionCommand(f.owner, mutationRequest("manifest-forbidden-write", 0))
	requireCode(t, err, codes.FailedPrecondition)
	if h.calls.Load() != 2 {
		t.Fatal("Запрещённая мутация вызвала гостя", h.calls.Load())
	}
	mutationCounts(t, f, 0, 0, 0, 0)
}

func TestExtensionP1ManifestPackageIdentity(t *testing.T) {
	bindings := mutationBindings("manifest_test", []byte{1})
	b := bindings["demo"]
	legacy := mutationPackageHash(b)
	b.Registration.SourceManifest = []byte(`{"plugin_id":"org.example.first"}`)
	first := mutationPackageHash(b)
	b.Registration.SourceManifest = []byte(`{"plugin_id":"org.example.second"}`)
	second := mutationPackageHash(b)
	if first == second || first == legacy || second == legacy {
		t.Fatal("Manifest не входит в идентичность пакета")
	}
}
