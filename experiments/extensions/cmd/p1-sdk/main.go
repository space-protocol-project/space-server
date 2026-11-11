package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/space-protocol-project/space-server/experiments/extensions/packagep1"
	"github.com/space-protocol-project/space-server/experiments/extensions/sdk"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	plugin := flag.String("plugin", "", "Идентификатор расширения")
	title := flag.String("title", "", "Название каркаса")
	license := flag.String("license", "LICENSE", "Файл лицензии")
	output := flag.String("out", "starter.zip", "Путь ZIP")
	keyFile := flag.String("key", "", "Файл seed Ed25519 в base64")
	fixture := flag.Bool("fixture", false, "Публичный тестовый ключ: не доверять в рабочей установке")
	flag.Parse()
	if *fixture == (*keyFile != "") {
		return errors.New("Выберите либо -key, либо -fixture")
	}
	key, err := signingKey(*keyFile, *fixture)
	if err != nil {
		return err
	}
	licenseBytes, err := os.ReadFile(*license)
	if err != nil {
		return err
	}
	payload, err := sdk.Starter(*plugin, *title, licenseBytes)
	if err != nil {
		return err
	}
	raw, err := packagep1.Pack(payload, key)
	if err != nil {
		return err
	}
	public := key.Public().(ed25519.PublicKey)
	trust := packagep1.Trust{Plugin: *plugin, Keys: map[string]ed25519.PublicKey{packagep1.Fingerprint(public): public}}
	verified, err := packagep1.Verify(raw, trust)
	if err != nil {
		return err
	}
	if err = os.WriteFile(*output, raw, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(*output+".publisher.key", []byte(base64.StdEncoding.EncodeToString(public)), 0644); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"sdk": sdk.Version, "plugin": *plugin, "digest": verified.Digest, "publisher": verified.Publisher, "fixture_key": *fixture})
}

func signingKey(path string, fixture bool) (ed25519.PrivateKey, error) {
	if fixture {
		seed := sha256.Sum256([]byte("Space P1 public SDK example; never trust in production"))
		return ed25519.NewKeyFromSeed(seed[:]), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("Неверный seed Ed25519")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
