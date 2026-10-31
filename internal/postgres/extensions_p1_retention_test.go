package postgres

import (
	"context"
	"fmt"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

func TestExtensionP1RetentionProtectsKeys(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "retention")
	h := new(mutationProbe)
	h.run = func(context.Context) ([]byte, error) { return mutationOutput(uint64(h.calls.Load() - 1)), nil }
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	old, recent := mutationRequest("retention-old-001", 0), mutationRequest("retention-new-001", 1)
	for _, r := range []*pb.ExecuteExtensionCommandRequest{old, recent} {
		if _, err := api.ExecuteExtensionCommand(f.owner, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.store.pool.Exec(f.ctx, `UPDATE extension_p1_receipts SET created_at=clock_timestamp()-interval '31 days' WHERE key=$1`, old.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	if count, err := RetireExtensionP1Receipts(f.ctx, f.url); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	_, err := api.ExecuteExtensionCommand(f.owner, old)
	requireCode(t, err, codes.FailedPrecondition)
	_, err = api.ReconcileExtensionCommand(f.owner, &pb.ReconcileExtensionCommandRequest{Command: old})
	requireCode(t, err, codes.FailedPrecondition)
	old.PayloadJson = []byte(`{ }`)
	_, err = api.ExecuteExtensionCommand(f.owner, old)
	requireCode(t, err, codes.AlreadyExists)
	requireReconciliation(t, api, f.owner, recent, true)
	if count, err := RetireExtensionP1Receipts(f.ctx, f.url); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if h.calls.Load() != 2 {
		t.Fatal("Удалённый ответ привёл к повторному исполнению")
	}
	mutationCounts(t, f, 2, 1, 2, 2)
	if err = CheckExtensionP1Backup(f.ctx, f.url, true); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = f.store.pool.QueryRow(f.ctx, `SELECT response FROM extension_p1_receipts WHERE retired`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) > 16 {
		t.Fatal("Тело ответа не удалено", len(raw))
	}
}

func TestExtensionP1RetentionUpgrade(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "retention_upgrade")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	r := mutationRequest("retention-upgrade-001", 0)
	if _, err := api.ExecuteExtensionCommand(f.owner, r); err != nil {
		t.Fatal(err)
	}
	// Воспроизводим прежний формат v1 без изменения сохранённого ответа.
	_, err := f.store.pool.Exec(f.ctx, `ALTER TABLE extension_p1_receipts DROP COLUMN retired;
ALTER TABLE extension_p1_meta DROP COLUMN history_id;
ALTER TABLE extension_p1_meta DROP CONSTRAINT extension_p1_meta_version_check;
UPDATE extension_p1_meta SET version=1;
ALTER TABLE extension_p1_meta ADD CONSTRAINT extension_p1_meta_version_check CHECK(version=1)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckExtensionP1Backup(f.ctx, f.url, false); err != nil {
		t.Fatal(err)
	}
	if _, err = RetireExtensionP1Receipts(f.ctx, f.url); err == nil {
		t.Fatal("v1 принят для удаления")
	}
	if _, err = f.store.NewExtensionP1MutationService(f.ctx, h, mutationBindings(c.Id, []byte{1})); err == nil {
		t.Fatal("v1 обновлён неявно")
	}
	for range 2 {
		if err = UpgradeExtensionP1Receipts(f.ctx, f.url); err != nil {
			t.Fatal(err)
		}
	}
	api = mutationClient(t, f, h, []byte{1}, c.Id)
	requireReconciliation(t, api, f.owner, r, true)
	// Старый конструктор выполнял именно эту вставку; v2 должен её отклонить.
	if _, err = f.store.pool.Exec(f.ctx, `INSERT INTO extension_p1_meta VALUES(true,1) ON CONFLICT DO NOTHING`); err == nil {
		t.Fatal("Старый writer принят")
	}
	if h.calls.Load() != 1 {
		t.Fatal("Миграция исполнила команду")
	}
}

func TestExtensionP1RetentionBatchAndRollback(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "retention_batch")
	h := new(mutationProbe)
	h.run = func(context.Context) ([]byte, error) { return mutationOutput(uint64(h.calls.Load() - 1)), nil }
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	for i := range 129 {
		if _, err := api.ExecuteExtensionCommand(f.owner, mutationRequest(fmt.Sprintf("retention-batch-%03d", i), uint64(i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.store.pool.Exec(f.ctx, `UPDATE extension_p1_receipts SET created_at=clock_timestamp()-interval '31 days'`); err != nil {
		t.Fatal(err)
	}
	// Ошибка в середине пакета не должна оставить частично удалённые ответы.
	if _, err := f.store.pool.Exec(f.ctx, `CREATE FUNCTION reject_retirement() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.key='retention-batch-001' THEN RAISE EXCEPTION 'test failure'; END IF; RETURN NEW; END$$;
CREATE TRIGGER reject_retirement BEFORE UPDATE ON extension_p1_receipts FOR EACH ROW EXECUTE FUNCTION reject_retirement()`); err != nil {
		t.Fatal(err)
	}
	if count, err := RetireExtensionP1Receipts(f.ctx, f.url); err == nil || count != 0 {
		t.Fatal("Частичное удаление принято", count, err)
	}
	var retired int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM extension_p1_receipts WHERE retired`).Scan(&retired); err != nil || retired != 0 {
		t.Fatal("Удаление не откатилось", retired, err)
	}
	if _, err := f.store.pool.Exec(f.ctx, `DROP TRIGGER reject_retirement ON extension_p1_receipts`); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{128, 1, 0} {
		if count, err := RetireExtensionP1Receipts(f.ctx, f.url); err != nil || count != want {
			t.Fatal(count, want, err)
		}
	}
	mutationCounts(t, f, 129, 1, 129, 129)
	if err := CheckExtensionP1Backup(f.ctx, f.url, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(f.ctx, `UPDATE extension_p1_receipts SET response='broken'::bytea WHERE key='retention-batch-000'`); err != nil {
		t.Fatal(err)
	}
	if _, err := RetireExtensionP1Receipts(f.ctx, f.url); err == nil {
		t.Fatal("Повреждение принято")
	}
}
