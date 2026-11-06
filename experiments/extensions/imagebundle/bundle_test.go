package imagebundle

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (string, Manifest, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	directory := t.TempDir()
	key, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Format: Format, Arch: "amd64", ABI: "space_plugin_p1", Revision: strings.Repeat("a", 40)}
	for _, name := range []string{"vmlinuz", "initramfs.gz"} {
		data := []byte("fixture:" + name)
		digest := sha256.Sum256(data)
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		manifest.Assets = append(manifest.Assets, Asset{name, int64(len(data)), hex.EncodeToString(digest[:])})
	}
	writeManifest(t, directory, manifest, private)
	return directory, manifest, key, private
}

func writeManifest(t *testing.T, directory string, manifest Manifest, private ed25519.PrivateKey) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeSigned(t, directory, raw, private)
}

func writeSigned(t *testing.T, directory string, raw []byte, private ed25519.PrivateKey) {
	t.Helper()
	for name, data := range map[string][]byte{"image.json": raw, "image.sig": ed25519.Sign(private, append([]byte(Domain), raw...))} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshot(t *testing.T) {
	source, _, key, _ := fixture(t)
	snapshot, err := Prepare(source, t.TempDir(), key, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := os.WriteFile(filepath.Join(source, "vmlinuz"), []byte("changed after verification"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(snapshot.Directory, "vmlinuz"))
	if err != nil || string(raw) != "fixture:vmlinuz" {
		t.Fatal("snapshot changed", err)
	}
	if !validHex(snapshot.Digest, 32) || snapshot.Arch != "amd64" {
		t.Fatal(snapshot)
	}
}

func rejected(t *testing.T, source string, key ed25519.PublicKey, arch string) {
	t.Helper()
	parent := t.TempDir()
	if snapshot, err := Prepare(source, parent, key, arch); err == nil {
		snapshot.Close()
		t.Fatal("accepted invalid image")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("partial snapshot published", err)
	}
}

func TestManifestRefusals(t *testing.T) {
	cases := map[string]func(*Manifest){
		"version":         func(m *Manifest) { m.Format = "space.vm.image.v2" },
		"architecture":    func(m *Manifest) { m.Arch = "arm64" },
		"abi":             func(m *Manifest) { m.ABI = "unknown" },
		"revision":        func(m *Manifest) { m.Revision = "main" },
		"path":            func(m *Manifest) { m.Assets[0].Name = "../vmlinuz" },
		"absolute":        func(m *Manifest) { m.Assets[0].Name = "/vmlinuz" },
		"duplicate_asset": func(m *Manifest) { m.Assets[1].Name = m.Assets[0].Name },
		"size":            func(m *Manifest) { m.Assets[0].Size++ },
		"zero":            func(m *Manifest) { m.Assets[0].Size = 0 },
		"limit":           func(m *Manifest) { m.Assets[1].Size = 256<<20 + 1 },
		"digest":          func(m *Manifest) { m.Assets[1].SHA256 = strings.Repeat("0", 64) },
		"digest_encoding": func(m *Manifest) { m.Assets[0].SHA256 = "invalid" },
		"missing_asset":   func(m *Manifest) { m.Assets = m.Assets[:1] },
	}
	for name, alter := range cases {
		t.Run(name, func(t *testing.T) {
			source, manifest, key, private := fixture(t)
			alter(&manifest)
			writeManifest(t, source, manifest, private)
			rejected(t, source, key, "amd64")
		})
	}
}

func TestUntrustedBytes(t *testing.T) {
	for _, name := range []string{"vmlinuz", "initramfs.gz", "image.json", "image.sig", "key", "missing", "directory"} {
		t.Run(name, func(t *testing.T) {
			source, _, key, _ := fixture(t)
			switch name {
			case "key":
				key = make([]byte, ed25519.PublicKeySize)
			case "missing":
				if err := os.Remove(filepath.Join(source, "vmlinuz")); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Remove(filepath.Join(source, "vmlinuz")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(source, "vmlinuz"), 0700); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(filepath.Join(source, name), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			rejected(t, source, key, "amd64")
		})
	}
}

func TestNonCanonicalSignedJSON(t *testing.T) {
	for _, name := range []string{"extra", "duplicate", "whitespace", "oversized"} {
		t.Run(name, func(t *testing.T) {
			source, _, key, private := fixture(t)
			raw, err := os.ReadFile(filepath.Join(source, "image.json"))
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "extra":
				raw = append([]byte(`{"extra":0,`), raw[1:]...)
			case "duplicate":
				raw = append([]byte(`{"arch":"amd64",`), raw[1:]...)
			case "whitespace":
				raw = append(raw, '\n')
			case "oversized":
				raw = []byte(strings.Repeat(" ", 8193))
			}
			writeSigned(t, source, raw, private)
			rejected(t, source, key, "amd64")
		})
	}
}

func TestSymlinkRefusal(t *testing.T) {
	source, _, key, _ := fixture(t)
	path := filepath.Join(source, "vmlinuz")
	outside := filepath.Join(t.TempDir(), "kernel")
	if err := os.Rename(path, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	rejected(t, source, key, "amd64")
}

func TestARM64Contract(t *testing.T) {
	source, manifest, key, private := fixture(t)
	manifest.Arch = "arm64"
	writeManifest(t, source, manifest, private)
	snapshot, err := Prepare(source, t.TempDir(), key, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
}

func TestSignatureBoundary(t *testing.T) {
	source, _, key, private := fixture(t)
	raw, err := os.ReadFile(filepath.Join(source, "image.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, signature := range map[string][]byte{
		"without_domain": ed25519.Sign(private, raw),
		"short":          make([]byte, 63),
		"long":           make([]byte, 65),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(source, "image.sig"), signature, 0600); err != nil {
				t.Fatal(err)
			}
			rejected(t, source, key, "amd64")
		})
	}
}
