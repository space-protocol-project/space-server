package transport

import (
	"encoding/json"
	"net/http"
)

func WithOriginBinding(next http.Handler, serverID string, publicKey []byte, identityOrigin, endpoint, fingerprint, signature string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/.well-known/space-protocol" {
			next.ServeHTTP(w, r)
			return
		}
		document := discoveryDocument(serverID, publicKey, "", []bool{true})
		document["identity_origin"] = identityOrigin
		document["network_origin"] = endpoint
		document["tls_fingerprint"] = fingerprint
		document["origin_signature"] = signature
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(document)
	})
}
