package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/space-protocol-project/space-server/experiments/extensions/packagep1"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func limited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("Превышен размер входа")
	}
	return raw, nil
}

func run() error {
	archive := flag.String("archive", "", "Проверяемый ZIP")
	publisher := flag.String("publisher", "", "Внешний доверенный публичный ключ, base64")
	plugin := flag.String("plugin", "", "Ожидаемый идентификатор расширения")
	revoked := flag.Bool("revoked", false, "Ключ отозван текущей конфигурацией")
	flag.Parse()
	raw, err := limited(*archive, packagep1.MaxArchive)
	if err != nil {
		return err
	}
	encoded, err := limited(*publisher, 64)
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return errors.New("Неверный доверенный ключ")
	}
	id := packagep1.Fingerprint(key)
	v, err := packagep1.Verify(raw, packagep1.Trust{Plugin: *plugin, Keys: map[string]ed25519.PublicKey{id: key}, Revoked: map[string]bool{id: *revoked}})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"plugin": v.Manifest.PluginID, "digest": v.Digest, "publisher": v.Publisher})
}
