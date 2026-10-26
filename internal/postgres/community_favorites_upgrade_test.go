package postgres

import (
	"bytes"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCommunityFavoritesUpgradeFromSchema38(t *testing.T) {
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
	for i := 12; i < 38; i++ {
		if err = applyMigration(ctx, tx, i+1, migrationFiles[i]); err != nil {
			t.Fatal(err)
		}
	}
	avatar := avatarFixture(t, 8)
	if _, err = tx.Exec(ctx, "UPDATE memberships SET nickname='Участник',profile_avatar=$1,profile_revision=4 WHERE principal_id='u_owner'", avatar); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO channels(id,title,view_type) VALUES('reaction_forum','Форум','forum');
 INSERT INTO forum_threads(id,channel_id,author_id,author_name,title,body,idempotency_key,sequence)
 VALUES('thread_before','reaction_forum','u_owner','Участник','Тема','Прежний текст','before',1);`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before := legacySnapshot(t, ctx, pool)
	var oldThread string
	if err = pool.QueryRow(ctx, "SELECT row_to_json(t)::text FROM forum_threads t WHERE id='thread_before'").Scan(&oldThread); err != nil {
		t.Fatal(err)
	}
	store, identity, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if identity.ServerID != original.ServerID {
		t.Fatal("Миграция изменила идентичность")
	}
	after := legacySnapshot(t, ctx, pool)
	for table, value := range before {
		if after[table] != value {
			t.Errorf("Миграция изменила %s", table)
		}
	}
	var name, thread string
	var image []byte
	var revision int64
	err = pool.QueryRow(ctx, "SELECT nickname,profile_avatar,profile_revision FROM memberships WHERE principal_id='u_owner'").Scan(&name, &image, &revision)
	if err != nil || name != "Участник" || !bytes.Equal(image, avatar) || revision != 4 {
		t.Fatal("Миграция изменила профиль", err)
	}
	if err = pool.QueryRow(ctx, "SELECT row_to_json(t)::text FROM forum_threads t WHERE id='thread_before'").Scan(&thread); err != nil || thread != oldThread {
		t.Fatal("Миграция изменила содержимое форума", err)
	}
	var count, version int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM community_favorites").Scan(&count); err != nil || count != 0 {
		t.Fatal("Миграция создала избранное", err)
	}
	if err = pool.QueryRow(ctx, "SELECT max(version) FROM schema_migrations").Scan(&version); err != nil || version != SchemaVersion() {
		t.Fatal("Неверная схема", version, err)
	}
}
