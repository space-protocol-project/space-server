package home

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/internal/authn"
)

func TestExtensionP1RegistryArchiveRejectsInvalid(t *testing.T) {
	runtime := os.Getenv("SPACE_HOME_RUNTIME")
	if runtime == "" {
		t.Skip("Нужен настоящий домашний runtime")
	}
	for _, portable := range []bool{false, true} {
		name := "physical"
		if portable {
			name = "portable"
		}
		for _, missing := range []bool{false, true} {
			suffix := "_gap"
			if missing {
				suffix = "_missing"
			}
			t.Run(name+suffix, func(t *testing.T) { registryArchiveRejectsInvalid(t, runtime, portable, missing) })
		}
	}
}

func registryArchiveRejectsInvalid(t *testing.T, runtime string, portable, missing bool) {
	f := newHistoryBackupFixture(t, runtime)
	newRegistryArchiveProbe(t, f.store, f.member, authn.Token(f.member), f.host)
	f.close()
	backup := f.save(portable)
	if portable {
		backup = corruptPortableRegistryArchive(t, f, backup, missing)
	} else {
		withRegistryArchiveDatabase(t, runtime, filepath.Join(backup, "data"), func(address string) { corruptRegistryArchiveRows(t, address, missing) })
		if err := writeBackupManifest(backup); err != nil {
			t.Fatal(err)
		}
	}
	before := registryArchiveDigest(t, backup, portable)
	output := t.TempDir()
	destination := filepath.Join(output, "restored")
	var err error
	if portable {
		err = ImportPortable(runtime, backup, destination, "")
	} else {
		err = RestoreBackup(runtime, backup, destination)
	}
	if err == nil || (!strings.Contains(err.Error(), "Журнал жизненного цикла") && !strings.Contains(err.Error(), "Неполный или неизвестный реестр")) {
		t.Fatal("Нет отказа проверки реестра", err)
	}
	entries, readErr := os.ReadDir(output)
	if readErr != nil || len(entries) != 0 {
		t.Fatal("Неудачное восстановление опубликовало данные или оставило временные файлы", entries, readErr)
	}
	if registryArchiveDigest(t, backup, portable) != before {
		t.Fatal("Изменён исходный архив")
	}
	t.Logf("portable=%v missing_journal=%v valid_checksums=true rejected_before_publish=true source_unchanged=true", portable, missing)
}

func withRegistryArchiveDatabase(t *testing.T, runtime, data string, run func(string)) {
	t.Helper()
	address, stop, err := StartDatabase(runtime, data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	}()
	run(address)
}

func corruptRegistryArchiveRows(t *testing.T, address string, missing bool) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	sql := `DELETE FROM extension_p1_lifecycle_audit WHERE installation='disabling' AND revision=2`
	if missing {
		sql = `DROP TABLE extension_p1_lifecycle_audit`
	}
	if _, err = conn.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}

func corruptPortableRegistryArchive(t *testing.T, f *historyBackupFixture, source string, missing bool) string {
	t.Helper()
	unpacked := t.TempDir()
	if err := extractPortableArchive(source, unpacked); err != nil {
		t.Fatal(err)
	}
	manifest, err := verifyPortableManifest(unpacked)
	if err != nil {
		t.Fatal(err)
	}
	withRegistryArchiveDatabase(t, f.runtime, f.data, func(address string) {
		corruptRegistryArchiveRows(t, address, missing)
		if err := portableDatabaseTool(f.runtime, "pg_dump", address, unpacked, "--format=custom", "--no-owner", "--no-privileges", "--exclude-table-data=auth_sessions", "--file="+filepath.Join(unpacked, "database.dump")); err != nil {
			t.Fatal(err)
		}
	})
	manifest.Files["database.dump"], err = backupFileDigest(filepath.Join(unpacked, "database.dump"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(unpacked, "space-export.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.root, "invalid.zip")
	if err = writePortableArchive(unpacked, target); err != nil {
		t.Fatal(err)
	}
	return target
}

func registryArchiveDigest(t *testing.T, source string, portable bool) string {
	t.Helper()
	if portable {
		if _, err := InspectPortable(source); err != nil {
			t.Fatal("Неверная структура тестового архива", err)
		}
		digest, err := backupFileDigest(source)
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	if err := VerifyBackup(source); err != nil {
		t.Fatal("Неверные контрольные суммы физической копии", err)
	}
	digest, err := backupFileDigest(filepath.Join(source, "backup.json"))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
