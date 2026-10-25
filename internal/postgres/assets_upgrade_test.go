package postgres

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"testing"
)

func seedVersion12(t *testing.T, ctx context.Context, pool *pgxpool.Pool) Identity {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "CREATE TABLE schema_migrations(version integer PRIMARY KEY,checksum text NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	for i, file := range migrationFiles[:12] {
		if err = applyMigration(ctx, tx, i+1, file); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := loadIdentity(ctx, tx, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `
 INSERT INTO principals(id,root_public_key) VALUES('u_owner',decode(repeat('01',32),'hex')),('u_reader',decode(repeat('02',32),'hex'));
 INSERT INTO memberships(principal_id,role,blocked,revision) VALUES('u_owner','admin',false,4),('u_reader','reader',true,7);
 UPDATE space_settings SET title='Существующее пространство',registration_policy='closed',owner_id='u_owner',revision=9,icon_preset='spark';
 INSERT INTO contents(channel_id,sequence,id,text,idempotency_key,author_id,revision,created_at_unix_ms,updated_at_unix_ms,deleted,request_text_hash)
 VALUES('general',1,'message-1','После правки','old-1','u_owner',2,1000,3000,false,sha256(convert_to('До правки','UTF8'))),
 ('general',2,'message-2','','old-2','u_owner',3,2000,4000,true,sha256(convert_to('Удалённый текст','UTF8')));
 INSERT INTO events(channel_id,sequence,content_sequence,cursor,type) VALUES
 ('general',1,1,'event-1','content.created'),('general',2,2,'event-2','content.created'),
 ('general',3,1,'event-3','content.updated'),('general',4,2,'event-4','content.deleted');
 UPDATE channels SET next_sequence=3,next_event_sequence=5,revision=8,public_preview=false WHERE id='general';
 UPDATE channel_access SET can_read=false,can_write=false WHERE role='reader';`)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return identity
}

func legacySnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	tables := map[string]string{
		"server_state": "ARRAY[]::text[]", "principals": "ARRAY[]::text[]", "channels": "ARRAY['mls_group_id','voice_capacity']",
		"contents": "ARRAY['attachments','reactions','reply_to_id','search_text','mentions']", "events": "ARRAY[]::text[]", "channel_access": "ARRAY[]::text[]",
		"memberships": "ARRAY['nickname','storage_limit_bytes']", "space_settings": "ARRAY['storage_limit_bytes','member_storage_limit_bytes','attachments_enabled']",
	}
	result := map[string]string{}
	for table, excluded := range tables {
		var snapshot string
		query := "SELECT COALESCE(jsonb_agg(value ORDER BY value::text),'[]')::text FROM (SELECT to_jsonb(t)-" + excluded + " AS value FROM " + table + " t) rows"
		if err := pool.QueryRow(ctx, query).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		result[table] = snapshot
	}
	return result
}

func TestAssetsUpgradeFromReleasedSchema12(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	original := seedVersion12(t, ctx, pool)
	before := legacySnapshot(t, ctx, pool)
	store, identity, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if identity.ServerID != original.ServerID {
		t.Fatal("Изменилась идентичность сервера")
	}
	after := legacySnapshot(t, ctx, pool)
	for table, value := range before {
		if after[table] != value {
			t.Errorf("Миграция изменила прежние данные %s", table)
		}
	}
	var changedCapacity int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM channels WHERE voice_capacity <> 8").Scan(&changedCapacity); err != nil {
		t.Fatal(err)
	}
	if changedCapacity != 0 {
		t.Fatal("Прежним комнатам назначен неверный лимит")
	}
	var normalized, deletedNormalized string
	if err = pool.QueryRow(ctx, "SELECT (SELECT search_text FROM contents WHERE sequence=1 AND channel_id='general'),(SELECT search_text FROM contents WHERE sequence=2 AND channel_id='general')").Scan(&normalized, &deletedNormalized); err != nil {
		t.Fatal(err)
	}
	if normalized != "после правки" || deletedNormalized != "" {
		t.Fatal("Поисковая проекция старых сообщений неверна")
	}
	messages, err := store.List(ctx, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages.Contents) != 2 || messages.Contents[0].Revision != 2 || !messages.Contents[1].Deleted {
		t.Fatal("Утеряны правки или удаление", messages)
	}
	for _, message := range messages.Contents {
		if message.ReplyToId != "" {
			t.Fatal("Миграция добавила выдуманную ссылку ответа")
		}
		if len(message.Attachments) != 0 {
			t.Fatal("Появились выдуманные вложения")
		}
	}
	var total, member, version int64
	if err = pool.QueryRow(ctx, "SELECT storage_limit_bytes,member_storage_limit_bytes,(SELECT max(version) FROM schema_migrations) FROM space_settings").Scan(&total, &member, &version); err != nil {
		t.Fatal(err)
	}
	if total != 1<<30 || member != 1<<30 || version != int64(SchemaVersion()) {
		t.Fatal("Некорректная схема или начальные квоты", total, member, version)
	}
	next, err := store.Create(ctx, &pb.CreateContentRequest{ChannelId: "general", Text: "После обновления", IdempotencyKey: "new"})
	if err != nil || next.GetContent().GetSequence() != 3 {
		t.Fatal("Нарушен порядок сообщений", err)
	}
	restarted, _, err := Open(ctx, url)
	if err != nil {
		t.Fatal("Повторный запуск после миграции", err)
	}
	restarted.Close()
}
