package sdk

import (
	"bytes"
	"context"
	"os"
	"testing"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/tetratelabs/wazero"
)

func TestStarterABI(t *testing.T) {
	p, err := Starter("org.example.starter", "Пример", []byte("MIT"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter().WithMemoryLimitPages(4))
	defer r.Close(ctx)
	m, err := r.Instantiate(ctx, p["server.wasm"])
	if err != nil {
		t.Fatal(err)
	}
	allocated, err := m.ExportedFunction("space_alloc").Call(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Memory().Write(uint32(allocated[0]), []byte(`{}`)) {
		t.Fatal("Недоступна память")
	}
	result, err := m.ExportedFunction("space_handle").Call(ctx, allocated[0], 2)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := m.Memory().Read(uint32(result[0]>>32), uint32(result[0]))
	if !ok || extensions.CheckJSON(out) != nil || !bytes.Contains(out, []byte("space.ui.p1")) {
		t.Fatal("Неверный результат ABI")
	}
	if _, err = m.ExportedFunction("space_free").Call(ctx, allocated[0], 2); err != nil {
		t.Fatal(err)
	}
}

func TestStarterIsolated(t *testing.T) {
	if os.Getenv("SPACE_P1_SDK_WORKER") == "" {
		t.Skip("Нужен CI изолированного SDK")
	}
	p, err := Starter("org.example.starter", "Пример", []byte("MIT"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_SDK_CGROUP"), os.Getenv("SPACE_P1_SDK_WORKER"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	out, err := h.CompileAndCall(context.Background(), p["server.wasm"], []byte(`{}`), false, false)
	if err != nil || !bytes.Contains(out, []byte("space.ui.p1")) {
		t.Fatal(err, string(out))
	}
}
