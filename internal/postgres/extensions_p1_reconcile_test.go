package postgres

import (
	"context"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func requireReconciliation(t *testing.T, api pb.ExtensionP1ServiceClient, ctx context.Context, r *pb.ExecuteExtensionCommandRequest, confirmed bool) *pb.ExecuteExtensionCommandResponse {
	t.Helper()
	result, err := api.ReconcileExtensionCommand(ctx, &pb.ReconcileExtensionCommandRequest{Command: r})
	if err != nil {
		t.Fatal(err)
	}
	want := pb.ExtensionCommandOutcome_EXTENSION_COMMAND_OUTCOME_UNKNOWN
	if confirmed {
		want = pb.ExtensionCommandOutcome_EXTENSION_COMMAND_OUTCOME_CONFIRMED
	}
	if result.Outcome != want || (result.Receipt != nil) != confirmed || result.RetryWindowKnown || result.RetryWindowRemainingMs != 0 {
		t.Fatal("Некорректная сверка или неподтверждённое окно повторов", result)
	}
	return result.Receipt
}

func TestExtensionP1ReconcileReceipt(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "reconcile")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	r := mutationRequest("reconcile-command-001", 0)
	requireReconciliation(t, api, f.owner, r, false)
	mutationCounts(t, f, 0, 0, 0, 0)
	if h.calls.Load() != 0 {
		t.Fatal("Сверка вызвала WASM")
	}
	first, err := api.ExecuteExtensionCommand(f.owner, r)
	if err != nil {
		t.Fatal(err)
	}
	// Теряем ответ и открываем новый Store: сверка не исполняет команду вновь.
	reopened, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	svc, err := reopened.NewExtensionP1MutationService(f.ctx, h, mutationBindings(c.Id, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	api = extensionP1RPCClient(t, f, svc)
	receipt := requireReconciliation(t, api, f.owner, r, true)
	if !receipt.Replayed {
		t.Fatal("Квитанция не отмечена как повторная")
	}
	receipt.Replayed = false
	if !proto.Equal(first, receipt) {
		t.Fatal("Сверка изменила сохранённый результат")
	}
	// Устаревшая ревизия неизвестной команды не доказывает её исход.
	requireReconciliation(t, api, f.owner, mutationRequest("missing-command-001", 0), false)
	changed := proto.Clone(r).(*pb.ExecuteExtensionCommandRequest)
	changed.PayloadJson = []byte(`{ }`)
	_, err = api.ReconcileExtensionCommand(f.owner, &pb.ReconcileExtensionCommandRequest{Command: changed})
	requireCode(t, err, codes.AlreadyExists)
	if h.calls.Load() != 1 {
		t.Fatal("Сверка повторно вызвала WASM")
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}

func TestExtensionP1ReconcileAccess(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "reconcile_access")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	member, _ := f.enroll(t, true)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	r := mutationRequest("reconcile-access-001", 0)
	if _, err := api.ExecuteExtensionCommand(member, r); err != nil {
		t.Fatal(err)
	}
	requireReconciliation(t, api, member, r, true)
	// Даже владелец не получает квитанцию другого principal.
	requireReconciliation(t, api, f.owner, r, false)
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	for _, key := range []string{r.IdempotencyKey, "missing-command-001"} {
		_, err := api.ReconcileExtensionCommand(member, &pb.ReconcileExtensionCommandRequest{Command: mutationRequest(key, 0)})
		requireCode(t, err, codes.PermissionDenied)
	}
	if _, err := f.auth.Logout(member, &pb.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	_, err := api.ReconcileExtensionCommand(member, &pb.ReconcileExtensionCommandRequest{Command: r})
	requireCode(t, err, codes.Unauthenticated)
	if h.calls.Load() != 1 {
		t.Fatal("Сверка вызвала WASM")
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}

func TestExtensionP1ReconcileConcurrentCommand(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "reconcile_concurrent")
	entered, release := make(chan struct{}), make(chan struct{})
	h := &mutationProbe{run: func(ctx context.Context) ([]byte, error) {
		close(entered)
		select {
		case <-release:
			return mutationOutput(0), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	r := mutationRequest("reconcile-concurrent-001", 0)
	done := make(chan error, 1)
	go func() { _, err := api.ExecuteExtensionCommand(f.owner, r); done <- err }()
	waitMutationSignal(t, f.ctx, entered)
	type observation struct {
		result *pb.ReconcileExtensionCommandResponse
		err    error
	}
	checked := make(chan observation, 1)
	go func() {
		result, err := api.ReconcileExtensionCommand(f.owner, &pb.ReconcileExtensionCommandRequest{Command: r})
		checked <- observation{result, err}
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := <-checked
	if got.err != nil || got.result.GetOutcome() != pb.ExtensionCommandOutcome_EXTENSION_COMMAND_OUTCOME_CONFIRMED {
		t.Fatal(got)
	}
	if h.calls.Load() != 1 {
		t.Fatal("Конкурентная сверка вызвала WASM")
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}

func TestExtensionP1ReconcileInvalidAndUnavailable(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "reconcile_invalid")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	for _, r := range []*pb.ExecuteExtensionCommandRequest{nil, extensionP1Command(), mutationRequest("short", 0)} {
		_, err := api.ReconcileExtensionCommand(f.owner, &pb.ReconcileExtensionCommandRequest{Command: r})
		requireCode(t, err, codes.InvalidArgument)
	}
	r := mutationRequest("reconcile-invalid-001", 0)
	r.InstanceId = "unknown"
	_, err := api.ReconcileExtensionCommand(f.owner, &pb.ReconcileExtensionCommandRequest{Command: r})
	requireCode(t, err, codes.PermissionDenied)
	r.InstanceId = "demo"
	_, err = api.ReconcileExtensionCommand(f.ctx, &pb.ReconcileExtensionCommandRequest{Command: r})
	requireCode(t, err, codes.Unauthenticated)
	cancelled, cancel := context.WithCancel(f.owner)
	cancel()
	_, err = api.ReconcileExtensionCommand(cancelled, &pb.ReconcileExtensionCommandRequest{Command: r})
	requireCode(t, err, codes.Canceled)
	stateless := extensionP1Client(t, f, h, []byte{1}, c.Id)
	_, err = stateless.ReconcileExtensionCommand(f.owner, &pb.ReconcileExtensionCommandRequest{Command: r})
	requireCode(t, err, codes.FailedPrecondition)
	if h.calls.Load() != 0 {
		t.Fatal("Проверка вызвала WASM")
	}
	mutationCounts(t, f, 0, 0, 0, 0)
}

func TestExtensionP1ReconcileOldBackupAndCorruption(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "reconcile_backup")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	r := mutationRequest("reconcile-backup-001", 0)
	if _, err := api.ExecuteExtensionCommand(f.owner, r); err != nil {
		t.Fatal(err)
	}
	// Пустой protobuf декодируется, но не является подтверждением команды.
	if _, err := f.store.pool.Exec(f.ctx, `UPDATE extension_p1_receipts SET response=''::bytea`); err != nil {
		t.Fatal(err)
	}
	_, err := api.ReconcileExtensionCommand(f.owner, &pb.ReconcileExtensionCommandRequest{Command: r})
	requireCode(t, err, codes.DataLoss)
	// Моделируем состояние старой копии; формат/распаковка архива проверяются отдельно.
	if _, err := f.store.pool.Exec(f.ctx, `TRUNCATE extension_p1_data,extension_p1_receipts,extension_p1_outbox; UPDATE extension_p1_instances SET revision=0`); err != nil {
		t.Fatal(err)
	}
	requireReconciliation(t, api, f.owner, r, false)
	if h.calls.Load() != 1 {
		t.Fatal("Сверка восстановленного состояния повторила эффект")
	}
	mutationCounts(t, f, 0, 0, 0, 0)
}
