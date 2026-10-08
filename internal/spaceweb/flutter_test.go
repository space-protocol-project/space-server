package spaceweb

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlutterBuildIsConfinedAndSeparateFromLegacyCSP(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("Space Admin"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := FlutterHandler(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { handler.(interface{ Close() error }).Close() })
	for _, test := range []struct {
		path string
		code int
	}{{"/space/flutter/", 200}, {"/space/flutter/../secret", 404}, {"/space/flutter/missing", 404}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", test.path, nil))
		if response.Code != test.code {
			t.Fatal(test, response.Code)
		}
		if response.Code == 200 && !strings.Contains(response.Header().Get("Content-Security-Policy"), "wasm-unsafe-eval") {
			t.Fatal("CSP Flutter отсутствует")
		}
	}
	legacy := httptest.NewRecorder()
	Handler().ServeHTTP(legacy, httptest.NewRequest("GET", "/space", nil))
	if strings.Contains(legacy.Header().Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatal("CSP старой панели ослаблена")
	}
}
