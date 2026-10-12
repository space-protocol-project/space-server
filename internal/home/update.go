package home

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	"github.com/space-protocol-project/space-server/internal/postgres"
)

type UpdateCheck struct {
	postgres.Compatibility
	TLSFingerprint string `json:"tlsFingerprint"`
}

// Только локальная PostgreSQL; публичные HTTP/gRPC порты не открываются.
func CheckOrMigrate(runtime, data string, migrate bool) (result UpdateCheck, failure error) {
	if err := CheckStoppedDatabase(runtime, data); err != nil {
		return result, err
	}
	certificate, err := tls.LoadX509KeyPair(filepath.Join(data, "server.crt"), filepath.Join(data, "server.key"))
	if err != nil || len(certificate.Certificate) == 0 {
		return result, errors.New("Не удалось проверить прежний сертификат сервера")
	}
	fingerprint := sha256.Sum256(certificate.Certificate[0])
	result.TLSFingerprint = hex.EncodeToString(fingerprint[:])
	address, stop, err := StartDatabase(runtime, data)
	if err != nil {
		return result, err
	}
	defer func() { failure = errors.Join(failure, stop()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result.Compatibility, err = postgres.CheckCompatibility(ctx, address)
	if err != nil {
		return result, err
	}
	if !migrate {
		return result, nil
	}
	store, identity, err := postgres.Open(ctx, address)
	if err != nil {
		return result, err
	}
	store.Close()
	if identity.ServerID != result.ServerID {
		return result, errors.New("Миграция изменила идентичность сервера")
	}
	result.Compatibility, err = postgres.CheckCompatibility(ctx, address)
	return result, err
}
