// Создаёт только временную подпись CI; это не служба издателя и не ключ выпуска.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/space-protocol-project/space-server/experiments/extensions/imagebundle"
)

func main() {
	if os.Getenv("GITHUB_ACTIONS") != "true" || len(os.Args) != 3 {
		os.Exit(2)
	}
	if err := sign(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func sign(directory, revision string) error {
	key, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	manifest := imagebundle.Manifest{Format: imagebundle.Format, Arch: runtime.GOARCH, ABI: "space_plugin_p1", Revision: revision}
	for _, name := range []string{"vmlinuz", "initramfs.gz"} {
		bytes, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		digest := sha256.Sum256(bytes)
		manifest.Assets = append(manifest.Assets, imagebundle.Asset{Name: name, Size: int64(len(bytes)), SHA256: hex.EncodeToString(digest[:])})
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(directory, "image.json"), raw, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(directory, "image.sig"), ed25519.Sign(private, append([]byte(imagebundle.Domain), raw...)), 0600); err != nil {
		return err
	}
	fmt.Println(hex.EncodeToString(key))
	return nil
}
