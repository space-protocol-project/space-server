package postgres

import (
	"context"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func historyContext(t *testing.T, api pb.ExtensionP1ServiceClient, ctx context.Context) string {
	t.Helper()
	r, err := api.GetExtensionMutationContext(ctx, &pb.GetExtensionMutationContextRequest{InstanceId: "demo"})
	if err != nil || !extensionHistoryPattern.MatchString(r.GetHistoryId()) {
		t.Fatal(r, err)
	}
	return r.HistoryId
}

func TestExtensionP1HistoryRestoreFence(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "history_restore")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	old := historyContext(t, api, f.owner)
	if err := CheckExtensionP1Backup(f.ctx, f.url, false); err != nil {
		t.Fatal(err)
	}
	if historyContext(t, api, f.owner) != old {
		t.Fatal("Проверка копии сменила историю")
	}
	r := mutationRequest("history-command-001", 0)
	r.HistoryId = old
	if _, err := api.ExecuteExtensionCommand(f.owner, r); err != nil {
		t.Fatal(err)
	}
	// Моделируем состояние копии до команды; настоящие архивы проверяет home.
	if _, err := f.store.pool.Exec(f.ctx, `TRUNCATE extension_p1_data,extension_p1_receipts,extension_p1_outbox; UPDATE extension_p1_instances SET revision=0`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := CheckExtensionP1Backup(f.ctx, f.url, true); err != nil {
			t.Fatal(err)
		}
		current := historyContext(t, api, f.owner)
		if current == old {
			t.Fatal("Restore повторил идентификатор истории")
		}
		old = current
		_, err := api.ExecuteExtensionCommand(f.owner, r)
		requireCode(t, err, codes.FailedPrecondition)
		requireReconciliation(t, api, f.owner, r, false)
		mutationCounts(t, f, 0, 0, 0, 0)
	}
	if h.calls.Load() != 1 {
		t.Fatal("Старый запрос запустил WASM")
	}
	newRequest := mutationRequest("history-command-002", 0)
	newRequest.HistoryId = old
	if _, err := api.ExecuteExtensionCommand(f.owner, newRequest); err != nil {
		t.Fatal(err)
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}

func TestExtensionP1HistoryChangesDuringWorker(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "history_worker")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) {
		_, err := f.store.pool.Exec(f.ctx, `UPDATE extension_p1_meta SET history_id=replace(gen_random_uuid()::text,'-','')`)
		return mutationOutput(0), err
	}}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	_, err := api.ExecuteExtensionCommand(f.owner, mutationRequest("history-worker-001", 0))
	requireCode(t, err, codes.FailedPrecondition)
	mutationCounts(t, f, 0, 0, 0, 0)
}

func TestExtensionP1HistoryRequiresContextAndAccess(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "history_access")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	member, _ := f.enroll(t, true)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	raw := api.(*historyTestClient).ExtensionP1ServiceClient
	r := mutationRequest("history-access-001", 0)
	_, err := raw.ExecuteExtensionCommand(f.owner, r)
	requireCode(t, err, codes.FailedPrecondition)
	r.HistoryId = "invalid"
	_, err = raw.ExecuteExtensionCommand(f.owner, r)
	requireCode(t, err, codes.FailedPrecondition)
	_, err = api.GetExtensionMutationContext(f.ctx, &pb.GetExtensionMutationContextRequest{InstanceId: "demo"})
	requireCode(t, err, codes.Unauthenticated)
	historyContext(t, api, member)
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	_, err = api.GetExtensionMutationContext(member, &pb.GetExtensionMutationContextRequest{InstanceId: "demo"})
	requireCode(t, err, codes.PermissionDenied)
	if h.calls.Load() != 0 {
		t.Fatal("Получение контекста вызвало WASM")
	}
}

func TestExtensionP1HistoryLegacyUpgrade(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			f := newChannelFixture(t)
			c := f.create(t, "history_upgrade")
			h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
			api := mutationClient(t, f, h, []byte{1}, c.Id)
			r := mutationRequest("history-upgrade-001", 0)
			if _, err := api.ExecuteExtensionCommand(f.owner, r); err != nil {
				t.Fatal(err)
			}
			legacy := proto.Clone(r).(*pb.ExecuteExtensionCommandRequest)
			legacy.HistoryId = ""
			binding := extensionMutationBinding{packageHash: mutationPackageHash(mutationBindings(c.Id, []byte{1})["demo"])}
			digest := mutationDigest(binding, legacy)
			if _, err := f.store.pool.Exec(f.ctx, `UPDATE extension_p1_receipts SET digest=$1`, digest[:]); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.pool.Exec(f.ctx, `ALTER TABLE extension_p1_meta DROP COLUMN history_id; ALTER TABLE extension_p1_meta DROP CONSTRAINT extension_p1_meta_version_check`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.pool.Exec(f.ctx, `UPDATE extension_p1_meta SET version=$1`, version); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.pool.Exec(f.ctx, `ALTER TABLE extension_p1_meta ADD CONSTRAINT extension_p1_meta_version_check CHECK(version IN (1,2))`); err != nil {
				t.Fatal(err)
			}
			if version == 1 {
				if _, err := f.store.pool.Exec(f.ctx, `ALTER TABLE extension_p1_receipts DROP COLUMN retired`); err != nil {
					t.Fatal(err)
				}
			}
			if err := UpgradeExtensionP1Receipts(f.ctx, f.url); err != nil {
				t.Fatal(err)
			}
			raw := api.(*historyTestClient).ExtensionP1ServiceClient
			_, err := raw.ExecuteExtensionCommand(f.owner, legacy)
			requireCode(t, err, codes.FailedPrecondition)
			requireReconciliation(t, raw, f.owner, legacy, true)
			if h.calls.Load() != 1 {
				t.Fatal("Миграция запустила старую команду")
			}
		})
	}
}
