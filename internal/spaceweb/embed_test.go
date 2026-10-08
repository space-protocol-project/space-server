package spaceweb

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPanelAssetsAndCSP(t *testing.T) {
	for _, path := range []string{"/space", "/space/", "/space/app.mjs", "/space/channels.mjs", "/space/identity.mjs", "/space/styles.css"} {
		result := httptest.NewRecorder()
		Handler().ServeHTTP(result, httptest.NewRequest("GET", path, nil))
		if result.Code != 200 || !strings.Contains(result.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s: %d", path, result.Code)
		}
	}
	result := httptest.NewRecorder()
	Handler().ServeHTTP(result, httptest.NewRequest("GET", "/space/../admin.go", nil))
	if result.Code != 404 {
		t.Fatal(result.Code)
	}
}
