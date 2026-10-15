package transport

import (
	"encoding/json"
	"net/http"
)

func WithOriginBinding(next http.Handler, serverID string, publicKey []byte, identityOrigin, endpoint, fingerprint, signature string, extra ...map[string]any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/.well-known/space-protocol" {
			next.ServeHTTP(w, r)
			return
		}
		document := map[string]any{}
		for key, value := range discoveryDocument(serverID, publicKey, "", []bool{true}) {
			document[key] = value
		}
		document["identity_origin"] = identityOrigin
		document["network_origin"] = endpoint
		document["tls_fingerprint"] = fingerprint
		document["origin_signature"] = signature
		for _, fields := range extra {
			for key, value := range fields {
				document[key] = value
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(document)
	})
}
