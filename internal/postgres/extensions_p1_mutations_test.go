package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type mutationProbe struct {
	calls atomic.Int32
	run   func(context.Context) ([]byte, error)
}

func (h *mutationProbe) Invoke(ctx context.Context, _ extensions.Invocation) ([]byte, error) {
	h.calls.Add(1)
	return h.run(ctx)
}
func (*mutationProbe) Status() extensions.RuntimeStatus {
	return extensions.RuntimeStatus{State: "ready"}
}
func (*mutationProbe) Close() error { return nil }

func mutationBindings(channel string, module []byte) map[string]ExtensionP1MutationBinding {
	return map[string]ExtensionP1MutationBinding{"demo": {Installation: "install", Channel: channel, Registration: demo.Registration{Module: module, Manifest: demo.Manifest{HostAPI: "space.plugin.p1", ABI: "space_plugin_p1", UI: "space.ui.p1", Contribution: "overview", Query: "overview", Command: "acknowledge"}}, Events: []string{"note_changed"}}}
}
func mutationClient(t *testing.T, f *channelFixture, host extensions.Runtime, module []byte, channel string) pb.ExtensionP1ServiceClient {
	t.Helper()
	svc, err := f.store.NewExtensionP1MutationService(f.ctx, host, mutationBindings(channel, module))
	if err != nil {
		t.Fatal(err)
	}
	return extensionP1RPCClient(t, f, svc)
}
func mutationRequest(key string, revision uint64) *pb.ExecuteExtensionCommandRequest {
	r := extensionP1Command()
	r.Effect = pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_MUTATION
	r.IdempotencyKey = key
	r.ExpectedStateRevision = revision
	return r
}
func mutationOutput(revision uint64) []byte {
	p := &pb.ExtensionMutationPlan{ExpectedStateRevision: revision, Operations: []*pb.ExtensionMutationOperation{{Key: "note", Change: &pb.ExtensionMutationOperation_PutJson{PutJson: []byte(`{"value":1}`)}}}, Events: []*pb.ExtensionMutationEvent{{Type: "note_changed", PayloadJson: []byte(`{}`)}}}
	plan, _ := protojson.Marshal(p)
	result, _ := json.Marshal(map[string]any{"ui_api": "space.ui.p1", "document": json.RawMessage(`{"ui_api":"space.ui.p1"}`), "plan": json.RawMessage(plan)})
	return result
}
func mutationCounts(t *testing.T, f *channelFixture, wantRevision, wantData, wantReceipts, wantEvents int) {
	t.Helper()
	var revision, data, receipts, events int
	err := f.store.pool.QueryRow(f.ctx, `SELECT revision,(SELECT count(*) FROM extension_p1_data),(SELECT count(*) FROM extension_p1_receipts),(SELECT count(*) FROM extension_p1_outbox) FROM extension_p1_instances WHERE installation='install' AND instance='demo'`).Scan(&revision, &data, &receipts, &events)
	if err != nil || revision != wantRevision || data != wantData || receipts != wantReceipts || events != wantEvents {
		t.Fatalf("state=%d/%d/%d/%d err=%v", revision, data, receipts, events, err)
	}
}

func TestExtensionP1MutationReplayAndRestart(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "mut_replay")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	r := mutationRequest("replay-command-001", 0)
	first, err := api.ExecuteExtensionCommand(f.owner, r)
	if err != nil || first.GetStateRevision() != 1 || first.GetReplayed() {
		t.Fatal(first, err)
	}
	// Не используем первый ответ: моделируем его потерю после commit и новый Store.
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
	again, err := api.ExecuteExtensionCommand(f.owner, r)
	if err != nil || !again.GetReplayed() {
		t.Fatal(again, err)
	}
	again.Replayed = false
	if !proto.Equal(first, again) || h.calls.Load() != 1 {
		t.Fatal("Повтор изменил результат или вызвал WASM")
	}
	mutationCounts(t, f, 1, 1, 1, 1)
	changed := proto.Clone(r).(*pb.ExecuteExtensionCommandRequest)
	changed.PayloadJson = []byte(`{ }`)
	_, err = api.ExecuteExtensionCommand(f.owner, changed)
	requireCode(t, err, codes.AlreadyExists)
	_, err = api.ExecuteExtensionCommand(f.owner, mutationRequest("stale-command-001", 0))
	requireCode(t, err, codes.Aborted)
	if h.calls.Load() != 1 {
		t.Fatal("Конфликт вызвал WASM")
	}
}

func TestExtensionP1MutationConcurrentDuplicate(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "mut_parallel")
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
	r := mutationRequest("parallel-command-001", 0)
	type result struct {
		response *pb.ExecuteExtensionCommandResponse
		err      error
	}
	results := make(chan result, 2)
	call := func() { response, err := api.ExecuteExtensionCommand(f.owner, r); results <- result{response, err} }
	go call()
	<-entered
	go call()
	close(release)
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.response.Replayed == b.response.Replayed || h.calls.Load() != 1 {
		t.Fatal(a, b, h.calls.Load())
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}

func TestExtensionP1MutationRevocationBeforeCommitAndReplay(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "mut_revoke")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	member, id := f.enroll(t, true)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) {
		_, err := f.store.pool.Exec(f.ctx, "UPDATE memberships SET blocked=true,revision=revision+1 WHERE principal_id=$1", id)
		return mutationOutput(0), err
	}}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	r := mutationRequest("revoked-command-001", 0)
	_, err := api.ExecuteExtensionCommand(member, r)
	requireCode(t, err, codes.PermissionDenied)
	mutationCounts(t, f, 0, 0, 0, 0)
	// Другой экземпляр executor с тем же хранилищем; теперь разрешённая команда.
	h2 := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api = mutationClient(t, f, h2, []byte{1}, c.Id)
	_, err = api.ExecuteExtensionCommand(f.owner, r)
	if err != nil {
		t.Fatal(err)
	}
	// Удаление сессии ядром запрещает даже выдачу сохранённой квитанции.
	if _, err = f.auth.Logout(f.owner, &pb.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	_, err = api.ExecuteExtensionCommand(f.owner, r)
	requireCode(t, err, codes.Unauthenticated)
	mutationCounts(t, f, 1, 1, 1, 1)
}

func TestExtensionP1MutationRollbackAndQuota(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "mut_rollback")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	r := mutationRequest("rollback-command-001", 0)
	_, err := f.store.pool.Exec(f.ctx, `CREATE FUNCTION extension_p1_reject_event() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'CI injected outbox failure'; END$$;
 CREATE TRIGGER reject_event BEFORE INSERT ON extension_p1_outbox FOR EACH ROW EXECUTE FUNCTION extension_p1_reject_event()`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.ExecuteExtensionCommand(f.owner, r)
	requireCode(t, err, codes.Internal)
	mutationCounts(t, f, 0, 0, 0, 0)
	if _, err = f.store.pool.Exec(f.ctx, "DROP TRIGGER reject_event ON extension_p1_outbox"); err != nil {
		t.Fatal(err)
	}
	// Лимит ключей проверяется после применения плана, но откатывает весь commit.
	_, err = f.store.pool.Exec(f.ctx, `INSERT INTO extension_p1_data SELECT 'install','demo','key_'||n,'{}'::bytea FROM generate_series(1,256) n`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.ExecuteExtensionCommand(f.owner, r)
	requireCode(t, err, codes.ResourceExhausted)
	mutationCounts(t, f, 0, 256, 0, 0)
	if _, err = f.store.pool.Exec(f.ctx, "DELETE FROM extension_p1_data"); err != nil {
		t.Fatal(err)
	}
	_, err = api.ExecuteExtensionCommand(f.owner, r)
	if err != nil {
		t.Fatal(err)
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}

func TestExtensionP1MutationTrapAndMalformedPlan(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  []byte
		err  error
		code codes.Code
	}{
		{"trap", nil, errors.New("private guest failure"), codes.Internal},
		{"invalid", []byte(`{"ui_api":"space.ui.p1","document":{},"plan":{}}`), nil, codes.InvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newChannelFixture(t)
			c := f.create(t, "mut_fail")
			h := &mutationProbe{run: func(context.Context) ([]byte, error) { return test.raw, test.err }}
			api := mutationClient(t, f, h, []byte{1}, c.Id)
			_, err := api.ExecuteExtensionCommand(f.owner, mutationRequest("failure-command-001", 0))
			requireCode(t, err, test.code)
			mutationCounts(t, f, 0, 0, 0, 0)
		})
	}
}

func TestExtensionP1MutationRealWASM(t *testing.T) {
	worker := os.Getenv("SPACE_P1_WORKER")
	if worker == "" {
		t.Skip("Нужен изолированный CI")
	}
	f := newChannelFixture(t)
	c := f.create(t, "mut_real")
	host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), worker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	module, err := os.ReadFile(os.Getenv("SPACE_P1_MUTATION_MODULE"))
	if err != nil {
		t.Fatal(err)
	}
	api := mutationClient(t, f, host, module, c.Id)
	r := mutationRequest("wasm-mutation-001", 0)
	a, err := api.ExecuteExtensionCommand(f.owner, r)
	if err != nil || a.GetStateRevision() != 1 {
		t.Fatal(a, err)
	}
	b, err := api.ExecuteExtensionCommand(f.owner, r)
	if err != nil || !b.GetReplayed() {
		t.Fatal(b, err)
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}

func TestExtensionP1MutationInstanceIsolation(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "mut_instances")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	bindings := mutationBindings(c.Id, []byte{1})
	bindings["other"] = bindings["demo"]
	svc, err := f.store.NewExtensionP1MutationService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	api := extensionP1RPCClient(t, f, svc)
	for _, instance := range []string{"demo", "other"} {
		r := mutationRequest("instance-command-001", 0)
		r.InstanceId = instance
		result, err := api.ExecuteExtensionCommand(f.owner, r)
		if err != nil || result.GetReplayed() || result.GetStateRevision() != 1 {
			t.Fatal(result, err)
		}
	}
	var data, receipts, events int
	err = f.store.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM extension_p1_data),(SELECT count(*) FROM extension_p1_receipts),(SELECT count(*) FROM extension_p1_outbox)`).Scan(&data, &receipts, &events)
	if err != nil || data != 2 || receipts != 2 || events != 2 || h.calls.Load() != 2 {
		t.Fatal(data, receipts, events, err)
	}
}
