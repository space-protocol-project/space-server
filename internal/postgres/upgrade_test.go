package postgres

import (
	"bytes"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"testing"
)

func TestUpgradePreservesIdentityAndBackfillsEvents(t *testing.T) {
	ctx, databaseURL := isolatedDatabase(t)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "CREATE TABLE schema_migrations(version integer PRIMARY KEY,checksum text NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if err = applyMigration(ctx, tx, 1, "001_initial.sql"); err != nil {
		t.Fatal(err)
	}
	original, err := loadIdentity(ctx, tx, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO contents(channel_id,sequence,id,text,idempotency_key) VALUES('general',1,'message-1','Старое сообщение','old')"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE channels SET next_sequence=2"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	store, identity, err := openManual(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	channel, err := loadChannel(ctx, store.pool, "general", "")
	if err != nil || channel.Title != "Общий чат" || channel.Revision != 1 || !channel.PublicPreview {
		t.Fatalf("Миграция канала: %v %v", channel, err)
	}
	if identity.ServerID != original.ServerID || !bytes.Equal(identity.PublicKey, original.PublicKey) {
		t.Fatal("Upgrade изменил identity")
	}
	events, err := store.ListEvents(ctx, &pb.ListEventsRequest{ChannelId: "general"})
	if err != nil || len(events.GetEvents()) != 1 || events.Events[0].Content.Text != "Старое сообщение" || events.Events[0].Content.AuthorId != "legacy-demo" {
		t.Fatalf("Backfill: %v %v", events, err)
	}
}
