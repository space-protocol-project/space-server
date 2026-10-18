package home

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/internal/postgres"
)

func TestScheduledLiveBackupRestoresDatabase(t *testing.T) {
	runtime := os.Getenv("SPACE_HOME_RUNTIME")
	if runtime == "" {
		t.Skip("Нужен комплект PostgreSQL")
	}
	root := t.TempDir()
	data := filepath.Join(root, "data")
	address, stop, err := StartDatabase(runtime, data)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ctx := context.Background()
	store, identity, err := postgres.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	connection, err := pgx.Connect(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	if _, err := connection.Exec(ctx, `CREATE TABLE scheduled_probe(value bytea); INSERT INTO scheduled_probe VALUES(decode('000102ff','hex'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := Certificate(data, net.ParseIP("203.0.113.10")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"ip":"203.0.113.10","port":8443}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupJSON(filepath.Join(root, "backup-policy.json"), BackupPolicy{1, true, true, 24, 7, 30, 0}); err != nil {
		t.Fatal(err)
	}
	options := ScheduledBackupOptions{Data: data, Runtime: runtime, Address: address, ServerID: identity.ServerID}
	if err := RunScheduledBackupCycle(ctx, options); err != nil {
		t.Fatal(err)
	}
	state := scheduledState(t, options)
	if state.Code != "ok" || state.Count != 1 {
		t.Fatalf("Копия не создана: %+v", state)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO scheduled_probe VALUES(decode('aabb','hex'))`); err != nil {
		t.Fatal("Работающая база остановлена", err)
	}
	folder := filepath.Join(root, "scheduled-backups")
	items, _, err := listScheduled(ctx, folder, identity.ServerID)
	if err != nil || len(items) != 1 {
		t.Fatalf("Копия не проверяется: %v", err)
	}
	imported := filepath.Join(t.TempDir(), "restored")
	if err := ImportPortable(runtime, filepath.Join(folder, items[0].Name), imported, ""); err != nil {
		t.Fatal(err)
	}
	restored, stopRestored, err := StartDatabase(runtime, filepath.Join(imported, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer stopRestored()
	copyDB, err := pgx.Connect(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close(ctx)
	var value string
	var count int
	if err := copyDB.QueryRow(ctx, `SELECT count(*),min(encode(value,'hex')) FROM scheduled_probe`).Scan(&count, &value); err != nil || count != 1 || value != "000102ff" {
		t.Fatalf("Данные снимка: %d %s %v", count, value, err)
	}
	restoredIdentity, err := postgres.CheckCompatibility(ctx, restored)
	if err != nil || restoredIdentity.ServerID != identity.ServerID {
		t.Fatal("Потеря идентичности", err)
	}
}

func TestScheduledStopCancelsRunningDump(t *testing.T) {
	options, _, _ := scheduledFixture(t)
	started := make(chan struct{})
	options.Export = func(ctx context.Context, _, _, _, _ string) error { close(started); <-ctx.Done(); return ctx.Err() }
	stop := StartScheduledBackups(context.Background(), options)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Копия не запустилась")
	}
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Остановка не отменила выгрузку")
	}
}
