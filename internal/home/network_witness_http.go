package home

import (
	"net/http"
	"time"
)

// Handler применяется только к публичному TLS-входу, включая тест в отдельных сетях.
func (witness *NetworkWitness) Handler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !witnessRequest(r) {
			handler.ServeHTTP(w, r)
			return
		}
		response := &witnessResponse{ResponseWriter: w, status: http.StatusOK}
		handler.ServeHTTP(response, r)
		if response.status == http.StatusOK {
			witness.Observe(r.Host, r.RemoteAddr, r.TLS.ServerName, time.Now())
		}
	})
}

func witnessRequest(r *http.Request) bool {
	if r.TLS == nil || !r.TLS.HandshakeComplete || r.Method != http.MethodGet || r.URL.Path != "/.well-known/space-protocol" {
		return false
	}
	// Прокси не даёт надёжных сведений о сети исходного участника.
	for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Real-IP", "Via"} {
		if len(r.Header.Values(header)) > 0 {
			return false
		}
	}
	return true
}

type witnessResponse struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *witnessResponse) WriteHeader(status int) {
	if !w.written {
		w.status = status
		w.written = true
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *witnessResponse) Write(content []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(content)
}
func (w *witnessResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
