// Package imagebundle проверяет образ Linux-гостя до запуска VM.
package imagebundle

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const Domain = "space.p1.vm-image.v1\x00"
const Format = "space.vm.image.p1"

var ErrImage = errors.New("untrusted VM image")

type Asset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Format   string  `json:"format"`
	Arch     string  `json:"arch"`
	ABI      string  `json:"abi"`
	Revision string  `json:"revision"`
	Assets   []Asset `json:"assets"`
}

type Snapshot struct {
	Directory string `json:"directory"`
	Digest    string `json:"manifest_sha256"`
	Arch      string `json:"arch"`
}

func (s Snapshot) Close() error { return os.RemoveAll(s.Directory) }

func validHex(value string, length int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == length && hex.EncodeToString(decoded) == value
}

// Verify требует ключ из доверенной конфигурации вызывающего, а не из пакета.
// Канонические байты запрещают дубликаты, лишние поля и неоднозначность JSON.
func Verify(raw, signature []byte, key ed25519.PublicKey, arch string) (Manifest, error) {
	var manifest Manifest
	if len(raw) > 8192 || len(key) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return manifest, ErrImage
	}
	if !ed25519.Verify(key, append([]byte(Domain), raw...), signature) {
		return manifest, ErrImage
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, ErrImage
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || !bytes.Equal(raw, canonical) || !validManifest(manifest, arch) {
		return Manifest{}, ErrImage
	}
	return manifest, nil
}

func validManifest(manifest Manifest, arch string) bool {
	if (arch != "amd64" && arch != "arm64") || manifest.Arch != arch {
		return false
	}
	if manifest.Format != Format || manifest.ABI != "space_plugin_p1" || !validHex(manifest.Revision, 20) || len(manifest.Assets) != 2 {
		return false
	}
	limits := []struct {
		name string
		size int64
	}{{"vmlinuz", 64 << 20}, {"initramfs.gz", 256 << 20}}
	for index, limit := range limits {
		asset := manifest.Assets[index]
		if asset.Name != limit.name || asset.Size <= 0 || asset.Size > limit.size || !validHex(asset.SHA256, 32) {
			return false
		}
	}
	return true
}

func openRegular(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrImage
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, ErrImage
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, ErrImage
	}
	return file, nil
}

func readBounded(root *os.Root, name string, limit int64) ([]byte, error) {
	file, err := openRegular(root, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrImage
	}
	return raw, nil
}

func copyAsset(root *os.Root, directory string, asset Asset) error {
	source, err := openRegular(root, asset.Name)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(filepath.Join(directory, asset.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0400)
	if err != nil {
		return err
	}
	hash := sha256.New()
	count, copyError := io.Copy(io.MultiWriter(target, hash), io.LimitReader(source, asset.Size+1))
	closeError := target.Close()
	if copyError != nil || closeError != nil || count != asset.Size || hex.EncodeToString(hash.Sum(nil)) != asset.SHA256 {
		return ErrImage
	}
	return nil
}

// Prepare проверяет и копирует в новый закрытый каталог. Launcher получает
// только этот снимок: изменение исходника после проверки не меняет гостя.
func Prepare(source, parent string, key ed25519.PublicKey, arch string) (snapshot Snapshot, failure error) {
	root, err := os.OpenRoot(source)
	if err != nil {
		return snapshot, err
	}
	defer root.Close()
	raw, err := readBounded(root, "image.json", 8192)
	if err != nil {
		return snapshot, err
	}
	signature, err := readBounded(root, "image.sig", ed25519.SignatureSize)
	if err != nil {
		return snapshot, err
	}
	manifest, err := Verify(raw, signature, key, arch)
	if err != nil {
		return snapshot, err
	}
	directory, err := os.MkdirTemp(parent, "space-vm-image-")
	if err != nil {
		return snapshot, err
	}
	defer func() {
		if failure != nil {
			_ = os.RemoveAll(directory)
		}
	}()
	for _, asset := range manifest.Assets {
		if err := copyAsset(root, directory, asset); err != nil {
			return Snapshot{}, err
		}
	}
	digest := sha256.Sum256(raw)
	return Snapshot{Directory: directory, Digest: hex.EncodeToString(digest[:]), Arch: manifest.Arch}, nil
}
