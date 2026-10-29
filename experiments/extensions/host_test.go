package extensions

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func host(t *testing.T, interpreter bool) *Host {
	t.Helper()
	h, err := New(context.Background(), interpreter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	return h
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".wasm")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestEngines(t *testing.T) {
	for _, interpreter := range []bool{false, true} {
		name := "compiler"
		if interpreter {
			name = "interpreter"
		}
		t.Run(name, func(t *testing.T) {
			for _, name := range []string{"echo", "grow", "state", "start_denied", "host_denied"} {
				t.Run(name, func(t *testing.T) {
					h := host(t, interpreter)
					module, err := h.Load(context.Background(), fixture(t, name))
					if err != nil {
						t.Fatal(err)
					}
					again, err := h.Load(context.Background(), fixture(t, name))
					if err != nil || again != module {
						t.Fatal("Нет повторного использования компиляции", err)
					}
					for i := 0; i < 2; i++ {
						input := []byte(`{"version":1,"payload":{"text":"Привет"}}`)
						result, err := h.Call(context.Background(), module, input, false)
						if err != nil || string(result) != string(input) {
							t.Fatalf("%s: %s %v", name, result, err)
						}
					}
				})
			}
			for _, name := range []string{"loop", "alloc_loop", "free_loop", "start_loop", "host_wait", "pointer", "length", "overlap", "bad_json"} {
				t.Run(name, func(t *testing.T) {
					h := host(t, interpreter)
					module, err := h.Load(context.Background(), fixture(t, name))
					if err != nil {
						t.Fatal(err)
					}
					started := time.Now()
					_, err = h.Call(context.Background(), module, []byte(`{"ok":true}`), true)
					if err == nil {
						t.Fatal("Недопустимый модуль принят")
					}
					if time.Since(started) > 2*time.Second {
						t.Fatal("Отмена не ограничила выполнение")
					}
					healthy, err := h.Load(context.Background(), fixture(t, "echo"))
					if err != nil {
						t.Fatal(err)
					}
					if _, err = h.Call(context.Background(), healthy, []byte(`{}`), false); err != nil {
						t.Fatal("Ошибка повредила хост", err)
					}
				})
			}
		})
	}
	t.Logf("platform=%s/%s pages=%d concurrent=%d compiled=%d", runtime.GOOS, runtime.GOARCH, MemoryPages, MaxCalls, MaxCompiled)
}

func TestAdmissionAndCancellation(t *testing.T) {
	h := host(t, false)
	module, err := h.Load(context.Background(), fixture(t, "host_wait"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, MaxCalls)
	for i := 0; i < MaxCalls; i++ {
		go func() { _, err := h.Call(ctx, module, []byte(`{}`), true); results <- err }()
	}
	deadline := time.Now().Add(time.Second)
	for len(h.slots) != MaxCalls {
		if time.Now().After(deadline) {
			t.Fatal("Вызовы не стартовали")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err = h.Call(ctx, module, []byte(`{}`), true); !errors.Is(err, ErrQuota) {
		t.Fatal("Нет общей квоты", err)
	}
	cancel()
	for i := 0; i < MaxCalls; i++ {
		select {
		case err := <-results:
			if err == nil {
				t.Fatal("Отмена потеряна")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Завис host call")
		}
	}
	if len(h.slots) != 0 {
		t.Fatal("Квота не освобождена")
	}
}

func TestValidation(t *testing.T) {
	h := host(t, false)
	if _, err := h.Load(context.Background(), fixture(t, "unknown_import")); !errors.Is(err, ErrABI) {
		t.Fatal("WASI разрешён", err)
	}
	if _, err := h.Load(context.Background(), make([]byte, MaxModule+1)); !errors.Is(err, ErrQuota) {
		t.Fatal(err)
	}
	if _, err := h.Load(context.Background(), []byte("not wasm")); err == nil {
		t.Fatal("Некорректный бинарник принят")
	}
	for _, raw := range [][]byte{[]byte(`{"a":1,"a":2}`), []byte(`{"a":1,"\u0061":2}`), []byte(`{"n":NaN}`), []byte(`{"n":1e999}`), []byte(`{} {}`), {255}, []byte(strings.Repeat("[", 10) + "0" + strings.Repeat("]", 10)), make([]byte, MaxJSON+1)} {
		if CheckJSON(raw) == nil {
			t.Fatalf("Некорректный JSON принят: %q", raw)
		}
	}
}

func TestCompilationQuota(t *testing.T) {
	h := host(t, false)
	source := fixture(t, "echo")
	for i := 0; i <= MaxCompiled; i++ {
		binary := append(append([]byte(nil), source...), 0, 3, 1, byte('a'+i), 0)
		_, err := h.Load(context.Background(), binary)
		if i < MaxCompiled && err != nil {
			t.Fatal(err)
		}
		if i == MaxCompiled && !errors.Is(err, ErrQuota) {
			t.Fatal("Нет квоты компиляций", err)
		}
	}
}
