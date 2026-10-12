package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestCompatibilityRequiresExplicitDatabase(t *testing.T) {
	if _, err := CheckCompatibility(context.Background(), ""); err == nil {
		t.Fatal("Проверка без явной базы разрешена")
	}
}

func TestCompatibilityReadsOldSchemaWithoutMigrating(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "CREATE TABLE schema_migrations(version integer PRIMARY KEY, checksum text NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	for i, file := range migrationFiles[:9] {
		if err = applyMigration(ctx, tx, i+1, file); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := loadIdentity(ctx, tx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := CheckCompatibility(ctx, url)
	if err != nil || result.SchemaVersion != 9 || result.TargetSchemaVersion != SchemaVersion() || result.ServerID != identity.ServerID {
		t.Fatalf("Проверка старой базы: %+v %v", result, err)
	}
	var version int
	if err = conn.QueryRow(ctx, "SELECT max(version) FROM schema_migrations").Scan(&version); err != nil || version != 9 {
		t.Fatalf("Проверка изменила базу: %d %v", version, err)
	}
	var iconColumn bool
	if err = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='space_settings' AND column_name='icon_preset')").Scan(&iconColumn); err != nil || iconColumn {
		t.Fatalf("Миграция иконки выполнена во время проверки: %v %v", iconColumn, err)
	}
}

func TestCompatibilityRejectsDamagedAndFutureHistory(t *testing.T) {
	for _, mutation := range []string{
		"UPDATE schema_migrations SET checksum='invalid' WHERE version=1",
		"DELETE FROM schema_migrations WHERE version=2",
		"INSERT INTO schema_migrations(version,checksum) VALUES(999,'future')",
	} {
		t.Run(mutation, func(t *testing.T) {
			ctx, url := isolatedDatabase(t)
			store, _, err := Open(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, err = store.pool.Exec(ctx, mutation); err != nil {
				t.Fatal(err)
			}
			if _, err = CheckCompatibility(ctx, url); err == nil {
				t.Fatal("Небезопасная история принята")
			}
		})
	}
}
