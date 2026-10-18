package home

import (
	"archive/zip"
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/internal/postgres"
)

func TestPortableSpaceRoundTrip(t *testing.T) {
	runtime := os.Getenv("SPACE_HOME_RUNTIME")
	if runtime == "" {
		t.Skip("Нужен домашний runtime")
	}
	root := t.TempDir()
	data := filepath.Join(root, "data")
	address, stop, err := StartDatabase(runtime, data)
	if err != nil {
		t.Fatal(err)
	}
	active := true
	defer func() {
		if active {
			_ = stop()
		}
	}()
	store, identity, err := postgres.Open(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	connection, err := pgx.Connect(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(context.Background(), "CREATE TABLE transfer_probe(value text);INSERT INTO transfer_probe VALUES('Пространство на другом компьютере')"); err != nil {
		t.Fatal(err)
	}
	connection.Close(context.Background())
	certificate, err := Certificate(data, net.ParseIP("203.0.113.10"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"ip":"203.0.113.10","port":8443}`), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "space.zip")
	if err := ExportPortable(runtime, data, archive); err == nil {
		t.Fatal("Работающая база экспортирована")
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	active = false
	if err := ExportPortable(runtime, data, archive); err != nil {
		t.Fatal(err)
	}
	report, err := InspectPortable(archive)
	if err != nil || report.ServerID != identity.ServerID || report.SchemaVersion != postgres.SchemaVersion() {
		t.Fatalf("Проверка настоящего архива: %+v %v", report, err)
	}
	imported := filepath.Join(root, "another-computer")
	if err := ImportPortable(runtime, archive, imported, ""); err != nil {
		t.Fatal(err)
	}
	if err := ImportPortable(runtime, archive, imported, ""); err == nil {
		t.Fatal("Существующий сервер перезаписан")
	}
	importedData := filepath.Join(imported, "data")
	address, stop, err = StartDatabase(runtime, importedData)
	if err != nil {
		t.Fatal(err)
	}
	active = true
	restored, err := postgres.CheckCompatibility(context.Background(), address)
	if err != nil || restored.ServerID != identity.ServerID {
		t.Fatalf("Идентичность потеряна: %+v %v", restored, err)
	}
	connection, err = pgx.Connect(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := connection.QueryRow(context.Background(), "SELECT value FROM transfer_probe").Scan(&value); err != nil {
		t.Fatal(err)
	}
	connection.Close(context.Background())
	if value != "Пространство на другом компьютере" {
		t.Fatal("Данные потеряны")
	}
	restoredCertificate, err := Certificate(importedData, net.ParseIP("203.0.113.10"))
	if err != nil || !bytes.Equal(certificate.Certificate[0], restoredCertificate.Certificate[0]) {
		t.Fatal("TLS изменён", err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	active = false
	changed := filepath.Join(root, "new-ip")
	if err := ImportPortable(runtime, archive, changed, "203.0.113.11"); err != nil {
		t.Fatal(err)
	}
	stableOrigin, err := IdentityOrigin(filepath.Join(changed, "data"), "https://203.0.113.11:8443")
	if err != nil || stableOrigin != "https://203.0.113.10:8443" {
		t.Fatal("Постоянная идентичность потеряна", err)
	}
	renewed, err := Certificate(filepath.Join(changed, "data"), net.ParseIP("203.0.113.10"))
	if err != nil || !bytes.Equal(certificate.Certificate[0], renewed.Certificate[0]) {
		t.Fatal("При смене IP изменился закреплённый TLS", err)
	}
	digest, err := backupFileDigest(archive)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := os.ReadFile(filepath.Join(changed, "import-source.sha256"))
	if err != nil || string(receipt) != digest {
		t.Fatal("Импорт не подтвердил исходный архив", err)
	}
}

func TestPortableArchiveRejectsUnexpectedEntries(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "data/postgres/PG_VERSION", "database.dump"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "test.zip")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(file)
			for _, entry := range []string{"database.dump", "config.json", "server.crt", "server.key", name} {
				target, _ := writer.Create(entry)
				target.Write([]byte("test"))
			}
			writer.Close()
			file.Close()
			destination := filepath.Join(root, "output")
			os.Mkdir(destination, 0700)
			if err := extractPortableArchive(archive, destination); err == nil {
				t.Fatal("Недопустимый архив принят")
			}
			if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
				t.Fatal("Выход за пределы назначения")
			}
		})
	}
}

func TestPortableRestoreFailureDoesNotPublish(t *testing.T) {
	runtime := os.Getenv("SPACE_HOME_RUNTIME")
	if runtime == "" {
		t.Skip("Нужен домашний runtime")
	}
	// Состав и хеши корректны, но содержимое database.dump не является дампом PostgreSQL.
	archive := inspectionFixture(t, func(*portableManifest, map[string][]byte) {})
	if _, err := InspectPortable(archive); err != nil {
		t.Fatal(err)
	}
	before, err := backupFileDigest(archive)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	destination := filepath.Join(root, "restored")
	if err := ImportPortable(runtime, archive, destination, ""); err == nil {
		t.Fatal("Невосстановимый дамп принят")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("Неудачное восстановление опубликовало каталог", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("После сбоя остались временная база или секреты", err)
	}
	after, err := backupFileDigest(archive)
	if err != nil || after != before {
		t.Fatal("Восстановление изменило исходный архив", err)
	}
}
