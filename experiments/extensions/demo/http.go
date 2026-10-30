package demo

import (
	"context"
	"encoding/json"
	"errors"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"io"
	"net/http"
	"strings"
)

// Handler предназначен только для loopback-стенда с доверенным адаптером авторизации.
func (d *Dispatcher) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost || r.URL.Path != "/dispatch" {
			writeError(w, ErrRequest)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			writeError(w, ErrRequest)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, extensions.MaxJSON))
		if err != nil {
			writeError(w, ErrRequest)
			return
		}
		request, err := Decode(body)
		if err != nil {
			writeError(w, err)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			writeError(w, ErrDenied)
			return
		}
		credential := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		output, err := d.Dispatch(r.Context(), credential, request)
		if err != nil {
			writeError(w, err)
			return
		}
		_, _ = w.Write(output)
	})
}

func writeError(w http.ResponseWriter, err error) {
	code, status := "extension_failed", http.StatusBadGateway
	switch {
	case errors.Is(err, ErrDenied):
		code, status = "permission_denied", http.StatusForbidden
	case errors.Is(err, ErrRequest):
		code, status = "invalid_request", http.StatusBadRequest
	case errors.Is(err, ErrVersion):
		code, status = "unsupported_version", http.StatusConflict
	case errors.Is(err, extensions.ErrIsolation):
		code, status = "unavailable", http.StatusServiceUnavailable
	case errors.Is(err, extensions.ErrQuota):
		code, status = "quota_exceeded", http.StatusTooManyRequests
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code, status = "deadline", http.StatusGatewayTimeout
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}
