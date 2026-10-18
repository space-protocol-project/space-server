package home

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/internal/postgres"
)

// pg_dump сам открывает согласованный MVCC-снимок, файлы PostgreSQL не копируются.
func exportScheduled(ctx context.Context, runtime, data, address, destination string) error {
	for _, source := range []string{filepath.Join(data, "server.crt"), filepath.Join(data, "server.key"), filepath.Join(filepath.Dir(data), "config.json")} {
		info, err := os.Lstat(source)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Ссылки в конфигурации копии запрещены")
		}
	}
	staging, err := os.MkdirTemp(filepath.Dir(destination), ".partial-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := protectBackupDirectory(staging); err != nil {
		return err
	}
	identity, err := postgres.CheckCompatibility(ctx, address)
	if err != nil {
		return err
	}
	if err := portableDatabaseToolContext(ctx, runtime, "pg_dump", address, staging, "--format=custom", "--no-owner", "--no-privileges", "--exclude-table-data=auth_sessions", "--file="+filepath.Join(staging, "database.dump")); err != nil {
		return err
	}
	for _, name := range []string{"server.crt", "server.key"} {
		if err := copyFile(filepath.Join(data, name), filepath.Join(staging, name)); err != nil {
			return err
		}
	}
	if err := copyFile(filepath.Join(filepath.Dir(data), "config.json"), filepath.Join(staging, "config.json")); err != nil {
		return err
	}
	manifest := portableManifest{1, identity.SchemaVersion, identity.ServerID, map[string]string{}}
	for _, name := range portableNames {
		digest, err := backupFileDigest(filepath.Join(staging, name))
		if err != nil {
			return err
		}
		manifest.Files[name] = digest
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, "space-export.json"), encoded, 0600); err != nil {
		return err
	}
	return writePortableArchive(staging, destination)
}

func scheduledDatabaseBytes(ctx context.Context, address string) (uint64, error) {
	connection, err := pgx.Connect(ctx, address)
	if err != nil {
		return 0, err
	}
	defer connection.Close(context.Background())
	var size uint64
	err = connection.QueryRow(ctx, "SELECT pg_database_size(current_database())").Scan(&size)
	return size, err
}
