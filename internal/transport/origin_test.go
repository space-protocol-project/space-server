package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOriginGuard(t *testing.T) {
	handler := RestrictOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), "http://127.0.0.1:18080")
	for _, test := range []struct {
		host, origin, method, contentType string
		expected                          int
	}{
		{"127.0.0.1:18080", "", "GET", "", 204},
		{"127.0.0.1:18080", "http://127.0.0.1:18080", "POST", "application/json", 204},
		{"evil.example", "", "GET", "", 403},
		{"127.0.0.1:18080", "https://evil.example", "POST", "application/json", 403},
		{"127.0.0.1:18080", "http://127.0.0.1:18080", "POST", "text/plain", 415},
	} {
		request := httptest.NewRequest(test.method, "http://"+test.host+"/api/v1/space/setup/claim", nil)
		request.Header.Set("Origin", test.origin)
		request.Header.Set("Content-Type", test.contentType)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != test.expected {
			t.Fatalf("%+v: %d", test, recorder.Code)
		}
	}
}
