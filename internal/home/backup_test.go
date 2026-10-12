package home

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/internal/postgres"
)

func backupFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data", "postgres", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"data/postgres/PG_VERSION": "17", "data/database-password": "fixture", "data/server.key": "same-key"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := inventory(root)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backup.json"), bytes, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestBackupInventoryRejectsCorruption(t *testing.T) {
	for _, mutation := range []string{"changed", "missing", "extra", "directory", "manifest"} {
		t.Run(mutation, func(t *testing.T) {
			root := backupFixture(t)
			if err := VerifyBackup(root); err != nil {
				t.Fatal(err)
			}
			key := filepath.Join(root, "data", "server.key")
			var err error
			switch mutation {
			case "changed":
				err = os.WriteFile(key, []byte("other-key"), 0600)
			case "missing":
				err = os.Remove(key)
			case "extra":
				err = os.WriteFile(filepath.Join(root, "extra"), []byte("unexpected"), 0600)
			case "directory":
				err = os.Remove(filepath.Join(root, "data", "postgres", "empty"))
			case "manifest":
				err = os.WriteFile(filepath.Join(root, "backup.json"), []byte(`{"format":999}`), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyBackup(root); err == nil {
				t.Fatal("Повреждённая копия принята")
			}
		})
	}
}

func TestBackupNeverOverwritesExistingDestination(t *testing.T) {
	destination := t.TempDir()
	if err := publishDirectory(destination, func(string) error { t.Fatal("Существующий каталог изменён"); return nil }); err == nil {
		t.Fatal("Перезапись разрешена")
	}
	source := t.TempDir()
	if err := outside(source, filepath.Join(source, "backup")); err == nil {
		t.Fatal("Рекурсивная копия разрешена")
	}
}

func TestStoppedDatabaseBackupAndRestore(t *testing.T) {
	runtime := os.Getenv("SPACE_HOME_RUNTIME")
	if runtime == "" {
		t.Skip("Реальная PostgreSQL проверяется на трёх runner")
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
	_, err = connection.Exec(context.Background(), "CREATE TABLE backup_probe(value text); INSERT INTO backup_probe VALUES('saved')")
	_ = connection.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := Certificate(data, net.ParseIP("203.0.113.10"))
	if err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"ip":"203.0.113.10","port":8443}`)
	if err := os.WriteFile(filepath.Join(root, "config.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "backup")
	if err := CreateBackup(runtime, data, backup); err == nil {
		t.Fatal("Работающая база скопирована")
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	active = false
	if err := CreateBackup(runtime, data, backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err := RestoreBackup(runtime, backup, restored); err != nil {
		t.Fatal(err)
	}
	if restoredConfig, err := os.ReadFile(filepath.Join(restored, "config.json")); err != nil || string(restoredConfig) != string(config) {
		t.Fatal("Конфигурация потеряна", err)
	}
	restoredData := filepath.Join(restored, "data")
	address, stop, err = StartDatabase(runtime, restoredData)
	if err != nil {
		t.Fatal(err)
	}
	active = true
	store, nextIdentity, err := postgres.Open(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if identity.ServerID != nextIdentity.ServerID {
		t.Fatal("Идентичность потеряна")
	}
	nextCertificate, err := Certificate(restoredData, net.ParseIP("203.0.113.10"))
	if err != nil {
		t.Fatal(err)
	}
	if string(certificate.Certificate[0]) != string(nextCertificate.Certificate[0]) {
		t.Fatal("Сертификат заменён")
	}
	connection, err = pgx.Connect(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	var saved string
	err = connection.QueryRow(context.Background(), "SELECT value FROM backup_probe").Scan(&saved)
	_ = connection.Close(context.Background())
	if err != nil || saved != "saved" {
		t.Fatalf("Данные потеряны: %q %v", saved, err)
	}
	if err := VerifyBackup(backup); err != nil {
		t.Fatal("Исходная копия изменена", err)
	}
}
