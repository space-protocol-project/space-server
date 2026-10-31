package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	"github.com/space-protocol-project/space-server/internal/extensionp1"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

func TestExtensionP1RetryLeaseExpiresDuringWorker(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "lease_worker")
	binding := mutationBindings(c.Id, []byte{1})["demo"]
	now := time.Now()
	backend := &extensionMutations{
		store: f.store, slots: make(chan struct{}, 2),
		bindings: map[string]extensionMutationBinding{"demo": {
			installation: "install", channel: c.Id, command: "acknowledge",
			packageHash: mutationPackageHash(binding), events: map[string]bool{"note_changed": true},
		}},
		leases: extensionRetryLeases{now: func() time.Time { return now }},
	}
	if err := backend.prepare(f.ctx); err != nil {
		t.Fatal(err)
	}
	h := &mutationProbe{run: func(context.Context) ([]byte, error) {
		now = now.Add(extensionRetryWindow)
		return mutationOutput(0), nil
	}}
	svc, err := extensionp1.NewWithMutations(h, f.store.ExtensionP1Authorizer(map[string]string{"demo": c.Id}), map[string]demo.Registration{"demo": binding.Registration}, backend)
	if err != nil {
		t.Fatal(err)
	}
	api := extensionP1RPCClient(t, f, svc)
	grant, err := api.GetExtensionMutationContext(f.owner, &pb.GetExtensionMutationContextRequest{InstanceId: "demo", RequestRetryWindow: true})
	if err != nil {
		t.Fatal(err)
	}
	r := mutationRequest("lease-expiry-00100", 0)
	r.HistoryId, r.RetryLeaseId = grant.HistoryId, grant.RetryLeaseId
	_, err = api.ExecuteExtensionCommand(f.owner, r)
	requireCode(t, err, codes.FailedPrecondition)
	mutationCounts(t, f, 0, 0, 0, 0)
	if h.calls.Load() != 1 {
		t.Fatal("Не проверено истечение во время WASM")
	}
}

func TestExtensionP1RetryLeaseLimits(t *testing.T) {
	now := time.Now()
	leases := extensionRetryLeases{now: func() time.Time { return now }}
	var first string
	for range 8 {
		id, err := leases.issue("legacy-demo", "demo", "history")
		if err != nil {
			t.Fatal(err)
		}
		first = id
	}
	_, err := leases.issue("legacy-demo", "demo", "history")
	requireCode(t, err, codes.ResourceExhausted)
	r := &pb.ExecuteExtensionCommandRequest{InstanceId: "demo", HistoryId: "history", RetryLeaseId: first}
	if err = leases.check(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.InstanceId = "other"
	requireCode(t, leases.check(context.Background(), r), codes.FailedPrecondition)
	r.InstanceId = "demo"
	now = now.Add(extensionRetryWindow)
	requireCode(t, leases.check(context.Background(), r), codes.FailedPrecondition)
	if _, err = leases.issue("legacy-demo", "demo", "history"); err != nil {
		t.Fatal(err)
	}
	if len(leases.entries) != 1 {
		t.Fatal("Просроченные окна не очищены")
	}
}

func TestExtensionP1RetryLeaseReplayAndRestart(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "lease_replay")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	grant, err := api.GetExtensionMutationContext(f.owner, &pb.GetExtensionMutationContextRequest{InstanceId: "demo", RequestRetryWindow: true})
	if err != nil || grant.GetRetryWindowRemainingMs() != 30000 || len(grant.GetRetryLeaseId()) != 32 {
		t.Fatal(grant, err)
	}
	r := mutationRequest("lease-command-001", 0)
	r.HistoryId, r.RetryLeaseId = grant.HistoryId, grant.RetryLeaseId
	// Первый ответ потерян для потребителя; повтор должен вернуть ту же квитанцию.
	if _, err = api.ExecuteExtensionCommand(f.owner, r); err != nil {
		t.Fatal(err)
	}
	result, err := api.ExecuteExtensionCommand(f.owner, r)
	if err != nil || !result.GetReplayed() || h.calls.Load() != 1 {
		t.Fatal(result, err, h.calls.Load())
	}
	restarted := mutationClient(t, f, h, []byte{1}, c.Id)
	_, err = restarted.ExecuteExtensionCommand(f.owner, r)
	requireCode(t, err, codes.FailedPrecondition)
	requireReconciliation(t, restarted, f.owner, r, true)
	mutationCounts(t, f, 1, 1, 1, 1)
	if h.calls.Load() != 1 {
		t.Fatal("Перезапуск повторил WASM")
	}
}

func TestExtensionP1RetryLeaseRejectsForeignPrincipal(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "lease_principal")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	member, _ := f.enroll(t, true)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	grant, err := api.GetExtensionMutationContext(f.owner, &pb.GetExtensionMutationContextRequest{InstanceId: "demo", RequestRetryWindow: true})
	if err != nil {
		t.Fatal(err)
	}
	r := mutationRequest("lease-principal-001", 0)
	r.HistoryId, r.RetryLeaseId = grant.HistoryId, grant.RetryLeaseId
	_, err = api.ExecuteExtensionCommand(member, r)
	requireCode(t, err, codes.FailedPrecondition)
	mutationCounts(t, f, 0, 0, 0, 0)
	if h.calls.Load() != 0 {
		t.Fatal("Чужое окно запустило WASM")
	}
}
