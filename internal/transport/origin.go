package transport

import (
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// Host/Origin задаются конфигурацией, не вычисляются из пользовательских headers.
func RestrictOrigin(next http.Handler, origin string) http.Handler {
	return restrictOrigin(next, origin, false)
}
func RestrictOriginWithConnect(next http.Handler, origin string) http.Handler {
	return restrictOrigin(next, origin, true)
}
func restrictOrigin(next http.Handler, origin string, connectEnabled bool) http.Handler {
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
		if !acceptRequestMedia(r, connectEnabled) {
			http.Error(w, "Нужен application/json", http.StatusUnsupportedMediaType)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func acceptRequestMedia(request *http.Request, connectEnabled bool) bool {
	if request.Method != http.MethodPost && request.Method != http.MethodPatch {
		return true
	}
	media, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	if media == "application/json" {
		return true
	}
	if !connectEnabled || !isConnectProcedure(request.URL.Path) {
		return false
	}
	switch media {
	case "application/proto", "application/connect+proto", "application/connect+json":
		return true
	default:
		return false
	}
}
