package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/space-protocol-project/space-server/internal/home"
)

func observeHomeNetwork(ctx context.Context, handler http.Handler, config homeHTTPConfig, serverID string) (http.Handler, *home.NetworkWitness, error) {
	endpoints, _, err := homeEndpointsForBinding(config.data, config.publicOrigin)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256(config.certificate.Certificate[0])
	witness, err := home.NewNetworkWitness(ctx, config.data, serverID, hex.EncodeToString(digest[:]), endpoints)
	if err != nil {
		return nil, nil, err
	}
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
	}), witness, nil
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
