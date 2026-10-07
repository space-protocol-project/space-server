package transport

import (
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// Host/Origin задаются конфигурацией, не вычисляются из пользовательских headers.
func RestrictOrigin(next http.Handler, origin string) http.Handler {
	expected, _ := url.Parse(origin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if expected == nil || !strings.EqualFold(r.Host, expected.Host) {
			http.Error(w, "Недопустимый Host", http.StatusForbidden)
			return
		}
		if source := r.Header.Get("Origin"); source != "" && source != origin {
			http.Error(w, "Недопустимый Origin", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || media != "application/json" {
				http.Error(w, "Нужен application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
