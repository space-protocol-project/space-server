package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/space-protocol-project/space-server/internal/postgres"
	"github.com/space-protocol-project/space-server/internal/transport"
)

type bindingSigner interface {
	SignOriginBinding(context.Context, string, string, string, string) (string, error)
	SignEndpoints(context.Context, string, []byte) (string, error)
}

// Объявление и разрешённые Host меняются одной операцией под блокировкой.
func bindHomeOrigin(ctx context.Context, next http.Handler, signer bindingSigner, identity postgres.Identity, origin, fallback string, cert tls.Certificate, data string, public ...bool) (http.Handler, error) {
	var mu sync.Mutex
	var cached http.Handler
	var checked time.Time
	var revision int64
	var digest string
	refresh := func(ctx context.Context) error {
		endpoints, version, err := homeEndpointsForBinding(data, fallback)
		if err != nil {
			return err
		}
		content, _ := json.Marshal(endpoints)
		key := string(content)
		if version < revision || version == revision && key != digest {
			return fmt.Errorf("Откат списка адресов")
		}
		if cached != nil && version == revision {
			return nil
		}
		endpoint := fallback
		if len(endpoints) > 0 {
			endpoint = endpoints[0].Origin()
		}
		handler, err := fixedHomeOrigin(ctx, next, signer, identity, origin, endpoint, cert, endpoints, version)
		if err != nil {
			return err
		}
		if len(public) > 0 && public[0] {
			origins := make([]string, len(endpoints))
			for i, e := range endpoints {
				origins[i] = e.Origin()
			}
			handler = transport.RestrictEndpointOrigins(handler, origins)
		}
		cached, revision, digest = handler, version, key
		return nil
	}
	if err := refresh(ctx); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		var err error
		if time.Since(checked) >= time.Second {
			bounded, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			err = refresh(bounded)
			cancel()
			if err == nil {
				checked = time.Now()
			}
		}
		handler := cached
		mu.Unlock()
		if err != nil {
			http.Error(w, "Список адресов временно недоступен", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}), nil
}
