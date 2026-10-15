package postgres

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
)

// Подпись связывает постоянную идентичность с новым адресом и закреплённым TLS.
func (s *Store) SignOriginBinding(ctx context.Context, serverID, identityOrigin, endpoint, fingerprint string) (string, error) {
	var seed []byte
	if err := s.pool.QueryRow(ctx, "SELECT signing_seed FROM server_state WHERE singleton=true AND server_id=$1", serverID).Scan(&seed); err != nil {
		return "", err
	}
	if len(seed) != ed25519.SeedSize {
		return "", errors.New("Повреждён ключ сервера")
	}
	payload, err := json.Marshal([]string{serverID, identityOrigin, endpoint, fingerprint})
	if err != nil {
		return "", err
	}
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(seed), append([]byte("space/origin-binding/v1\x00"), payload...))
	return base64.RawURLEncoding.EncodeToString(signature), nil
}

func (s *Store) SignEndpoints(ctx context.Context, serverID string, payload []byte) (string, error) {
	var seed []byte
	if err := s.pool.QueryRow(ctx, "SELECT signing_seed FROM server_state WHERE singleton=true AND server_id=$1", serverID).Scan(&seed); err != nil {
		return "", err
	}
	if len(seed) != ed25519.SeedSize {
		return "", errors.New("Повреждён ключ сервера")
	}
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), payload)), nil
}
