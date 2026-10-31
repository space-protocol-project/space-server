package postgres

import (
	"context"
	"testing"
)

func TestExtensionP1BackupRejectsPartialState(t *testing.T) {
	for _, sql := range []string{
		`DROP TABLE extension_p1_receipts`,
		`DELETE FROM extension_p1_receipts`,
		`UPDATE extension_p1_delivery SET ack_revision=999,ack_ordinal=0`,
		`UPDATE extension_p1_outbox SET revision=999`,
		`UPDATE extension_p1_receipts SET response='broken'::bytea`,
		`UPDATE extension_p1_receipts SET retired=true`,
		`UPDATE extension_p1_receipts SET response=decode('10021801','hex')`,
		`ALTER TABLE extension_p1_receipts DROP COLUMN retired`,
		`ALTER TABLE extension_p1_meta DROP CONSTRAINT extension_p1_meta_version_check; UPDATE extension_p1_meta SET version=1`,
		`CREATE TABLE extension_p1_unknown(value text)`,
	} {
		t.Run(sql, func(t *testing.T) {
			f, o := newExtensionOutboxFixture(t)
			e := claimExtensionEvent(t, o)
			if _, err := f.store.pool.Exec(f.ctx, sql); err != nil {
				t.Fatal(err)
			}
			if err := CheckExtensionP1Backup(f.ctx, f.url, true); err == nil {
				t.Fatal("Повреждённая копия принята")
			}
			var token string
			if err := f.store.pool.QueryRow(f.ctx, `SELECT lease_token FROM extension_p1_delivery`).Scan(&token); err != nil || token != e.Token {
				t.Fatal("Отказ изменил аренду", token, err)
			}
		})
	}
}

func TestExtensionP1BackupLegacyAndReadOnly(t *testing.T) {
	f := newChannelFixture(t)
	if err := CheckExtensionP1Backup(f.ctx, f.url, true); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM pg_tables WHERE schemaname=current_schema() AND left(tablename,13)='extension_p1_'`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	f, o := newExtensionOutboxFixture(t)
	e := claimExtensionEvent(t, o)
	if err := CheckExtensionP1Backup(f.ctx, f.url, false); err != nil {
		t.Fatal(err)
	}
	if err := o.Ack(f.ctx, e.Token); err != nil {
		t.Fatal("Проверка отозвала аренду источника", err)
	}
}

func TestExtensionP1BackupNoEventIDReuse(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "backup_branch")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	request := mutationRequest("backup-branch-001", 0)
	var ids [2]string
	for i := range 2 {
		if _, err := api.ExecuteExtensionCommand(f.owner, request); err != nil {
			t.Fatal(err)
		}
		if err := f.store.pool.QueryRow(f.ctx, `SELECT event_id FROM extension_p1_outbox`).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			// Моделируем состояние старой копии до команды. Сами архивы проверяет home.
			_, err := f.store.pool.Exec(f.ctx, `TRUNCATE extension_p1_data,extension_p1_receipts,extension_p1_outbox; UPDATE extension_p1_instances SET revision=0`)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if ids[0] == ids[1] {
		t.Fatal("Повтор ревизии повторно использовал ID события")
	}
}
