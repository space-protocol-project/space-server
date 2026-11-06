// Закрытая подготовка снимка; публичный ключ передаётся доверенной оболочкой.
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/space-protocol-project/space-server/experiments/extensions/imagebundle"
)

func main() {
	if err := prepare(); err != nil {
		fmt.Fprintln(os.Stderr, "VMImageUnavailable")
		os.Exit(1)
	}
}

func prepare() error {
	if len(os.Args) != 4 {
		return imagebundle.ErrImage
	}
	key, err := hex.DecodeString(os.Args[2])
	if err != nil {
		return err
	}
	snapshot, err := imagebundle.Prepare(os.Args[1], os.Args[3], ed25519.PublicKey(key), runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(snapshot); err != nil {
		_ = snapshot.Close()
		return err
	}
	return nil
}
