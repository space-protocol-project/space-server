package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

func TestParticipantProfileUpgradeFromSchema36(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	original := seedVersion12(t, ctx, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for i := 12; i < 36; i++ {
		if err = applyMigration(ctx, tx, i+1, migrationFiles[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE memberships SET nickname='Прежнее имя' WHERE principal_id='u_owner'"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before := legacySnapshot(t, ctx, pool)
	store, identity, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if identity.ServerID != original.ServerID {
		t.Fatal("Изменена идентичность сервера")
	}
	after := legacySnapshot(t, ctx, pool)
	for table, value := range before {
		if after[table] != value {
			t.Errorf("Изменены прежние данные %s", table)
		}
	}
	var name string
	var avatar []byte
	var revision int64
	var version int
	err = pool.QueryRow(ctx, "SELECT nickname,profile_avatar,profile_revision,(SELECT max(version) FROM schema_migrations) FROM memberships WHERE principal_id='u_owner'").Scan(&name, &avatar, &revision, &version)
	if err != nil || name != "Прежнее имя" || len(avatar) != 0 || revision != 0 || version != 37 {
		t.Fatalf("Миграция профиля: %q %d %d %v", name, revision, version, err)
	}
}
