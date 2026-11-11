package packagep1_test

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/space-protocol-project/space-server/experiments/extensions/packagep1"
	"github.com/space-protocol-project/space-server/experiments/extensions/sdk"
)

func fixture(t *testing.T) ([]byte, ed25519.PrivateKey, packagep1.Trust) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(make([]byte, 32)) // Только публичная тестовая фикстура.
	payload, err := sdk.Starter("org.example.starter", "Пример", []byte("MIT"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := packagep1.Pack(payload, key)
	if err != nil {
		t.Fatal(err)
	}
	public := key.Public().(ed25519.PublicKey)
	trust := packagep1.Trust{Plugin: "org.example.starter", Keys: map[string]ed25519.PublicKey{packagep1.Fingerprint(public): public}, Revoked: map[string]bool{}}
	return raw, key, trust
}

func TestPackageReproducible(t *testing.T) {
	raw, _, trust := fixture(t)
	again, _, _ := fixture(t)
	if !bytes.Equal(raw, again) {
		t.Fatal("Сборка не воспроизводится")
	}
	v, err := packagep1.Verify(raw, trust)
	if err != nil || len(v.Files) != 7 || v.Manifest.PluginID != trust.Plugin {
		t.Fatal(v, err)
	}
}

func TestPackageTrust(t *testing.T) {
	for _, mode := range []string{"unknown", "revoked", "subject", "rotation_unapproved", "rotation_approved", "rotation_old_revoked"} {
		t.Run(mode, func(t *testing.T) {
			raw, _, trust := fixture(t)
			var old string
			for id := range trust.Keys {
				old = id
			}
			switch mode {
			case "unknown":
				trust.Keys = map[string]ed25519.PublicKey{}
			case "revoked":
				trust.Revoked[old] = true
			case "subject":
				trust.Plugin = "org.example.other"
			default:
				seed := make([]byte, 32)
				seed[0] = 1
				key := ed25519.NewKeyFromSeed(seed)
				public := key.Public().(ed25519.PublicKey)
				trust.Revoked[old] = true
				if mode != "rotation_unapproved" {
					trust.Keys[packagep1.Fingerprint(public)] = public
				}
				if mode != "rotation_old_revoked" {
					payload, err := sdk.Starter(trust.Plugin, "Пример", []byte("MIT"))
					if err != nil {
						t.Fatal(err)
					}
					raw, err = packagep1.Pack(payload, key)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := packagep1.Verify(raw, trust)
			if (err == nil) != (mode == "rotation_approved") {
				t.Fatal(mode, err)
			}
		})
	}
}

func rewritten(t *testing.T, raw []byte, alter func(*zip.FileHeader, []byte) (*zip.FileHeader, []byte)) []byte {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, file := range r.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		header := file.FileHeader
		h, data := alter(&header, data)
		entry, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestPackageArchiveRejects(t *testing.T) {
	for _, mode := range []string{"tamper", "signature", "index", "path", "absolute", "duplicate", "symlink", "module_limit", "json_limit", "compressed_limit"} {
		t.Run(mode, func(t *testing.T) {
			raw, _, trust := fixture(t)
			raw = rewritten(t, raw, func(h *zip.FileHeader, data []byte) (*zip.FileHeader, []byte) {
				if h.Name != "LICENSE" {
					return h, data
				}
				switch mode {
				case "tamper":
					data = []byte("changed")
				case "signature":
					h.Name = "index.sig"
				case "index":
					h.Name = "index.json"
				case "path":
					h.Name = "../LICENSE"
				case "absolute":
					h.Name = "/LICENSE"
				case "duplicate":
					h.Name = "manifest.json"
				case "symlink":
					h.SetMode(os.ModeSymlink | 0777)
				case "module_limit":
					h.Name = "server.wasm"
					data = make([]byte, 65537)
				case "json_limit":
					data = make([]byte, 16385)
				case "compressed_limit":
					h.Method = zip.Deflate
					data = make([]byte, 2<<20)
				}
				return h, data
			})
			if _, err := packagep1.Verify(raw, trust); err == nil {
				t.Fatal("Принят повреждённый архив")
			}
		})
	}
}

func TestPackageSignedInvalidManifest(t *testing.T) {
	_, key, trust := fixture(t)
	payload, err := sdk.Starter(trust.Plugin, "Пример", []byte("MIT"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(payload["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	m["wasm_abi"] = "unknown"
	payload["manifest.json"], err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := packagep1.Pack(payload, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = packagep1.Verify(raw, trust); err == nil {
		t.Fatal("Подпись разрешила неизвестный ABI")
	}
}
