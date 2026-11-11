// Package packagep1 проверяет подписанный серверный пакет кандидата P1.
package packagep1

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/manifest"
)

const Domain = "space.package.index.p1\x00"
const MaxArchive = 1 << 20

var ErrPackage = errors.New("package_rejected")
var schemaName = regexp.MustCompile(`^schemas/[a-z][a-z0-9_-]{0,63}\.json$`)

type Index struct {
	Format string            `json:"format"`
	Files  map[string]string `json:"files"`
}

// Trust поступает только из текущей конфигурации хозяина, никогда из ZIP.
// Отозванный ключ имеет приоритет над разрешённым. Ротация требует явного
// изменения этой конфигурации; подпись новым ключом сама его не разрешает.
type Trust struct {
	Plugin  string
	Keys    map[string]ed25519.PublicKey
	Revoked map[string]bool
}

type Verified struct {
	Manifest  manifest.Manifest
	Publisher string
	Digest    string
	Files     map[string][]byte
}

func Fingerprint(key []byte) string { hash := sha256.Sum256(key); return hex.EncodeToString(hash[:]) }

func Verify(archive []byte, trust Trust) (Verified, error) {
	files, err := readArchive(archive)
	if err != nil {
		return Verified{}, err
	}
	key, err := base64.StdEncoding.DecodeString(string(files["publisher.key"]))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return Verified{}, ErrPackage
	}
	fingerprint := Fingerprint(key)
	if trust.Revoked[fingerprint] || !bytes.Equal(trust.Keys[fingerprint], key) {
		return Verified{}, ErrPackage
	}
	signature, err := base64.StdEncoding.DecodeString(string(files["index.sig"]))
	if err != nil || !ed25519.Verify(key, append([]byte(Domain), files["index.json"]...), signature) {
		return Verified{}, ErrPackage
	}
	index, err := verifyIndex(files)
	if err != nil {
		return Verified{}, err
	}
	m, err := manifest.Decode(files["manifest.json"])
	if err != nil || m.PluginID != trust.Plugin {
		return Verified{}, ErrPackage
	}
	if err = verifyPayload(files, index, m); err != nil {
		return Verified{}, err
	}
	return Verified{m, fingerprint, Fingerprint(archive), files}, nil
}

func verifyIndex(files map[string][]byte) (Index, error) {
	raw := files["index.json"]
	var index Index
	if extensions.CheckJSON(raw) != nil || json.Unmarshal(raw, &index) != nil || index.Format != "space.package.index.p1" {
		return index, ErrPackage
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 2 || fields["format"] == nil || fields["files"] == nil || index.Files == nil || len(index.Files)+3 != len(files) {
		return index, ErrPackage
	}
	for name, digest := range index.Files {
		if name == "index.json" || name == "index.sig" || name == "publisher.key" || Fingerprint(files[name]) != digest {
			return index, ErrPackage
		}
		if _, ok := files[name]; !ok {
			return index, ErrPackage
		}
	}
	return index, nil
}

func verifyPayload(files map[string][]byte, index Index, m manifest.Manifest) error {
	expected := map[string]bool{"manifest.json": true, "server.wasm": true, "LICENSE": true}
	for _, operations := range []map[string]string{m.Commands, m.Queries} {
		for _, path := range operations {
			expected[path] = true
			if extensions.CheckJSON(files[path]) != nil {
				return ErrPackage
			}
		}
	}
	if len(expected) != len(index.Files) || len(files["LICENSE"]) == 0 {
		return ErrPackage
	}
	for name := range expected {
		if _, ok := index.Files[name]; !ok {
			return ErrPackage
		}
	}
	module := files["server.wasm"]
	if len(module) < 8 || len(module) > extensions.MaxModule || !bytes.Equal(module[:8], []byte{0, 97, 115, 109, 1, 0, 0, 0}) {
		return ErrPackage
	}
	return nil
}

func readArchive(raw []byte) (map[string][]byte, error) {
	if len(raw) == 0 || len(raw) > MaxArchive {
		return nil, ErrPackage
	}
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(r.File) > 70 {
		return nil, ErrPackage
	}
	files := map[string][]byte{}
	total := 0
	for _, file := range r.File {
		if !allowedPath(file.Name) || !file.Mode().IsRegular() || files[file.Name] != nil {
			return nil, ErrPackage
		}
		data, err := readFile(file)
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > MaxArchive {
			return nil, ErrPackage
		}
		files[file.Name] = data
	}
	return files, nil
}

func allowedPath(name string) bool {
	for _, fixed := range []string{"index.json", "index.sig", "publisher.key", "manifest.json", "server.wasm", "LICENSE"} {
		if name == fixed {
			return true
		}
	}
	// Имена схем дополнительно связываются с проверенным manifest.
	return schemaName.MatchString(name)
}

func readFile(file *zip.File) ([]byte, error) {
	limit := extensions.MaxJSON
	if file.Name == "server.wasm" {
		limit = extensions.MaxModule
	}
	if file.UncompressedSize64 > uint64(limit) {
		return nil, ErrPackage
	}
	r, err := file.Open()
	if err != nil {
		return nil, ErrPackage
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil, ErrPackage
	}
	return raw, nil
}

// Pack фиксирует порядок, время и метод ZIP; одинаковый payload и ключ дают
// одинаковые байты. Закрытый ключ не входит в результат.
func Pack(payload map[string][]byte, key ed25519.PrivateKey) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, ErrPackage
	}
	files := map[string][]byte{}
	index := Index{Format: "space.package.index.p1", Files: map[string]string{}}
	for name, data := range payload {
		if !allowedPath(name) || name == "index.json" || name == "index.sig" || name == "publisher.key" {
			return nil, ErrPackage
		}
		files[name], index.Files[name] = bytes.Clone(data), Fingerprint(data)
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}
	files["index.json"] = raw
	files["index.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(Domain), raw...))))
	files["publisher.key"] = []byte(base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)))
	var output bytes.Buffer
	w := zip.NewWriter(&output)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			return nil, err
		}
		if _, err = entry.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
