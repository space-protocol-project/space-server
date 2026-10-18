package home

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/space-protocol-project/space-server/internal/postgres"
)

func inspectionFixture(t *testing.T, change func(*portableManifest, map[string][]byte)) string {
	t.Helper()
	files := map[string][]byte{}
	manifest := portableManifest{1, postgres.SchemaVersion(), "test-server", map[string]string{}}
	for _, name := range portableNames {
		files[name] = []byte("test content " + name)
		digest := sha256.Sum256(files[name])
		manifest.Files[name] = hex.EncodeToString(digest[:])
	}
	change(&manifest, files)
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	files["space-export.json"] = data
	path := filepath.Join(t.TempDir(), "space.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	for name, body := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInspectPortableReadOnly(t *testing.T) {
	path := inspectionFixture(t, func(*portableManifest, map[string][]byte) {})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectPortable(path)
	if err != nil {
		t.Fatal(err)
	}
	if report.ServerID != "test-server" || report.SchemaVersion != postgres.SchemaVersion() || report.Files != 5 || report.UnpackedBytes == 0 {
		t.Fatalf("Неверный отчёт: %+v", report)
	}
	after, _ := os.ReadFile(path)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("Проверка изменила исходный архив")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal("Проверка распаковала файлы на диск")
	}
}

func TestInspectPortableRejectsCorruption(t *testing.T) {
	cases := map[string]func(*portableManifest, map[string][]byte){
		"changed-file":       func(_ *portableManifest, files map[string][]byte) { files["database.dump"] = []byte("changed") },
		"extra-file":         func(_ *portableManifest, files map[string][]byte) { files["extra"] = []byte("extra") },
		"missing-file":       func(_ *portableManifest, files map[string][]byte) { delete(files, "server.key") },
		"schema":             func(m *portableManifest, _ map[string][]byte) { m.SchemaVersion++ },
		"missing-hash":       func(m *portableManifest, _ map[string][]byte) { delete(m.Files, "server.crt") },
		"oversized-metadata": func(_ *portableManifest, files map[string][]byte) { files["config.json"] = make([]byte, 65537) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := InspectPortable(inspectionFixture(t, change)); err == nil {
				t.Fatal("Повреждённый архив принят")
			}
		})
	}
}

func TestInspectPortableRejectsDamagedZipPayload(t *testing.T) {
	path := inspectionFixture(t, func(*portableManifest, map[string][]byte) {})
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	var offset int64
	for _, entry := range archive.File {
		if entry.Name == "database.dump" {
			offset, err = entry.DataOffset()
		}
	}
	archive.Close()
	if err != nil || offset == 0 {
		t.Fatal("Не найдено содержимое ZIP", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[offset] ^= 0xff
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectPortable(path); err == nil {
		t.Fatal("Повреждённые байты ZIP приняты")
	}
}
