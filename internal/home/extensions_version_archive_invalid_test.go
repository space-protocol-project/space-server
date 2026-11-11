package home

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestExtensionP1VersionArchiveInvalid(t *testing.T) {
	seed := os.Getenv("SPACE_P1_VERSION_SEED")
	if seed == "" {
		t.Skip("Нужен архив WASM-истории")
	}
	for _, portable := range []bool{false, true} {
		format := map[bool]string{false: "physical", true: "zip"}[portable]
		for _, kind := range []string{"digest", "gap", "module"} {
			t.Run(format+"/"+kind, func(t *testing.T) {
				versionArchiveInvalid(t, seed, portable, kind)
			})
		}
	}
}

func corruptVersionRows(t *testing.T, address, kind string) {
	t.Helper()
	queries := map[string]string{
		"digest": `UPDATE extension_p1_versions SET record_hash=repeat('0',64) WHERE sequence=1`,
		"gap":    `DELETE FROM extension_p1_versions WHERE sequence=1`,
		"module": `UPDATE extension_p1_upgrade_artifacts SET modules=convert_to('{}','UTF8')`,
	}
	if versionArchiveGroup() {
		queries = map[string]string{
			"digest": `UPDATE extension_p1_group_versions SET record_hash=repeat('0',64) WHERE sequence=1`,
			"gap":    `DELETE FROM extension_p1_group_versions WHERE sequence=1`,
			"module": `UPDATE extension_p1_group_jobs SET record=convert_to('{}','UTF8')`,
		}
	}
	conn, err := pgx.Connect(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if versionArchiveGroup() && kind == "module" {
		corruptGroupArchiveModule(t, conn)
		return
	}
	if _, err = conn.Exec(context.Background(), queries[kind]); err != nil {
		t.Fatal(err)
	}
}

func versionArchiveInvalid(t *testing.T, seed string, portable bool, kind string) {
	t.Helper()
	runtime := os.Getenv("SPACE_HOME_RUNTIME")
	source := filepath.Join(seed, "published.zip")
	initial := registryArchiveDigest(t, source, true)
	restored := filepath.Join(t.TempDir(), "source")
	if err := ImportPortable(runtime, source, restored, ""); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(restored, "data")
	target := filepath.Join(t.TempDir(), "bad")
	if portable {
		target += ".zip"
		staging := t.TempDir()
		if err := extractPortableArchive(source, staging); err != nil {
			t.Fatal(err)
		}
		manifest, err := verifyPortableManifest(staging)
		if err != nil {
			t.Fatal(err)
		}
		withRegistryArchiveDatabase(t, runtime, data, func(address string) {
			corruptVersionRows(t, address, kind)
			if err := portableDatabaseTool(runtime, "pg_dump", address, staging, "--format=custom", "--compress=none", "--no-owner", "--no-privileges", "--exclude-table-data=auth_sessions", "--file="+filepath.Join(staging, "database.dump")); err != nil {
				t.Fatal(err)
			}
		})
		manifest.Files["database.dump"], err = backupFileDigest(filepath.Join(staging, "database.dump"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(staging, "space-export.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err = writePortableArchive(staging, target); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := CreateBackup(runtime, data, target); err != nil {
			t.Fatal(err)
		}
		withRegistryArchiveDatabase(t, runtime, filepath.Join(target, "data"), func(address string) { corruptVersionRows(t, address, kind) })
		if err := writeBackupManifest(target); err != nil {
			t.Fatal(err)
		}
	}
	before := registryArchiveDigest(t, target, portable)
	output := t.TempDir()
	destination := filepath.Join(output, "restored")
	var err error
	if portable {
		err = ImportPortable(runtime, target, destination, "")
	} else {
		err = RestoreBackup(runtime, target, destination)
	}
	if err == nil {
		t.Fatal("Восстановлена повреждённая история")
	}
	entries, readErr := os.ReadDir(output)
	if readErr != nil || len(entries) != 0 {
		t.Fatal("Отказ оставил опубликованные или временные данные", readErr)
	}
	if registryArchiveDigest(t, target, portable) != before || registryArchiveDigest(t, source, true) != initial {
		t.Fatal("Изменён исходный архив")
	}
	t.Logf("portable=%v corruption=%s checksums_valid=true rejected_before_publish=true", portable, kind)
}
