package postgres

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
)

func lifecycleJournal(t *testing.T, f *channelFixture) string {
	t.Helper()
	var snapshot string
	err := f.store.pool.QueryRow(f.ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY installation,revision),'[]'::jsonb)::text FROM extension_p1_lifecycle_audit a`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func lifecycleJournalCount(t *testing.T, f *channelFixture, want int) {
	t.Helper()
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM extension_p1_lifecycle_audit`).Scan(&count); err != nil || count != want {
		t.Fatal(count, err)
	}
}

func TestExtensionP1LifecycleJournalActors(t *testing.T) {
	f := newChannelFixture(t)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	s, _ := managedFixture(t, f, h)
	managedChange(t, f, s, "enable", 1)
	managedChange(t, f, s, "disable", 2)
	if _, err := s.SystemTransition(f.ctx, "install", "finish_disable", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SystemTransition(f.ctx, "install", "quarantine", 4); err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "remove", 5)
	var valid bool
	err := f.store.pool.QueryRow(f.ctx, `SELECT count(*)=6 AND bool_and(generation=$1)
 AND count(*) FILTER(WHERE actor='checkpoint' AND revision=1 AND state='disabled' AND principal IS NULL)=1
 AND count(*) FILTER(WHERE actor='owner' AND principal=(SELECT principal_id FROM admin_audit WHERE action LIKE 'extension.p1.lifecycle/%' LIMIT 1))=3
 AND count(*) FILTER(WHERE actor='system' AND principal IS NULL)=2
 AND count(*) FILTER(WHERE action='finish_disable' AND previous_state='disabling' AND state='disabled' AND revision=4)=1
 AND count(*) FILTER(WHERE action='quarantine' AND previous_state='disabled' AND state='quarantined' AND revision=5)=1
 FROM extension_p1_lifecycle_audit`, s.Generation()).Scan(&valid)
	if err != nil || !valid {
		t.Fatal(valid, err)
	}
	lifecycleAuditCount(t, f, 3)
	before := lifecycleJournal(t, f)
	if err = CheckExtensionP1Backup(f.ctx, f.url, true); err != nil {
		t.Fatal(err)
	}
	if lifecycleJournal(t, f) != before {
		t.Fatal("Восстановление переписало историю")
	}
}

func TestExtensionP1LifecycleJournalRollback(t *testing.T) {
	for _, actor := range []string{"owner", "system"} {
		t.Run(actor, func(t *testing.T) {
			f := newChannelFixture(t)
			h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
			s, api := managedFixture(t, f, h)
			managedChange(t, f, s, "enable", 1)
			before := lifecycleJournal(t, f)
			_, err := f.store.pool.Exec(f.ctx, `CREATE FUNCTION reject_journal() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'fixture'; END$$;
 CREATE TRIGGER reject_journal BEFORE INSERT ON extension_p1_lifecycle_audit FOR EACH ROW EXECUTE FUNCTION reject_journal()`)
			if err != nil {
				t.Fatal(err)
			}
			if actor == "owner" {
				_, err = s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", "disable", 2)
			} else {
				_, err = s.SystemTransition(f.ctx, "install", "quarantine", 2)
			}
			requireCode(t, err, codes.Internal)
			lifecycleRegistryState(t, f, "enabled", 2)
			lifecycleAuditCount(t, f, 1)
			if lifecycleJournal(t, f) != before {
				t.Fatal("Неудачный переход изменил журнал")
			}
			if _, err = api.QueryExtension(f.owner, extensionP1Query()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExtensionP1LifecycleJournalMigration(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "journal_migration")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	bindings := mutationBindings(c.Id, []byte{1})
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	// Состояние старого реестра v1: данных о прежних системных переходах нет.
	_, err = f.store.pool.Exec(f.ctx, `DROP TABLE extension_p1_lifecycle_audit; UPDATE extension_p1_meta SET lifecycle_version=1;
 ALTER TABLE extension_p1_meta DROP CONSTRAINT extension_p1_meta_lifecycle_version_check;
 ALTER TABLE extension_p1_meta ADD CONSTRAINT extension_p1_meta_lifecycle_version_check CHECK(lifecycle_version IN (0,1))`)
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckExtensionP1Backup(f.ctx, f.url, false); err != nil {
		t.Fatal(err)
	}
	store, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	changed := mutationBindings(c.Id, []byte{1})
	changed["extra"] = changed["demo"]
	_, err = store.NewExtensionP1ManagedService(f.ctx, h, changed)
	requireCode(t, err, codes.FailedPrecondition)
	var rolledBack bool
	err = f.store.pool.QueryRow(f.ctx, `SELECT lifecycle_version=1 AND to_regclass('extension_p1_lifecycle_audit') IS NULL AND (SELECT count(*) FROM extension_p1_instances)=1 FROM extension_p1_meta`).Scan(&rolledBack)
	if err != nil || !rolledBack {
		t.Fatal("Миграция не откатилась", rolledBack, err)
	}
	next, err := store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	var action string
	var revision int64
	if err = f.store.pool.QueryRow(f.ctx, `SELECT action,revision FROM extension_p1_lifecycle_audit`).Scan(&action, &revision); err != nil || action != "checkpoint" || revision != 2 {
		t.Fatal(action, revision, err)
	}
	if _, err = next.SystemTransition(f.ctx, "install", "quarantine", 2); err != nil {
		t.Fatal(err)
	}
	lifecycleJournalCount(t, f, 2)
	before := lifecycleJournal(t, f)
	store.Close()
	store, _, err = Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.NewExtensionP1ManagedService(f.ctx, h, bindings); err != nil {
		t.Fatal(err)
	}
	if lifecycleJournal(t, f) != before {
		t.Fatal("Повторный запуск изменил журнал")
	}
	if err = CheckExtensionP1Backup(f.ctx, f.url, false); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionP1LifecycleJournalRejectsPartialBackup(t *testing.T) {
	for _, sql := range []string{
		`DROP TABLE extension_p1_lifecycle_audit`,
		`DELETE FROM extension_p1_lifecycle_audit WHERE revision=2`,
		`DELETE FROM extension_p1_lifecycle_audit WHERE revision=3`,
		`DELETE FROM extension_p1_lifecycle_audit WHERE revision=1`,
		`UPDATE extension_p1_installations SET state='disabled'`,
	} {
		t.Run(sql, func(t *testing.T) {
			f := newChannelFixture(t)
			h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
			s, _ := managedFixture(t, f, h)
			managedChange(t, f, s, "enable", 1)
			managedChange(t, f, s, "disable", 2)
			if _, err := f.store.pool.Exec(f.ctx, sql); err != nil {
				t.Fatal(err)
			}
			if err := CheckExtensionP1Backup(f.ctx, f.url, true); err == nil {
				t.Fatal("Повреждённый журнал принят")
			}
			if err := s.control.checkGeneration(f.ctx, "install"); err != nil {
				t.Fatal("Отказ изменил поколение", err)
			}
		})
	}
}
