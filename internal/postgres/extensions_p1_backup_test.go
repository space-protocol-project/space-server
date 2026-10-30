package postgres

import "testing"

func TestExtensionP1BackupRejectsPartialState(t *testing.T) {
	for _, sql := range []string{
		`DROP TABLE extension_p1_receipts`,
		`DELETE FROM extension_p1_receipts`,
		`UPDATE extension_p1_delivery SET ack_revision=999,ack_ordinal=0`,
		`UPDATE extension_p1_outbox SET revision=999`,
		`UPDATE extension_p1_receipts SET response='broken'::bytea`,
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
