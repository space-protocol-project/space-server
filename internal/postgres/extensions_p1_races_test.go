package postgres

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExtensionP1MutationConcurrentRevisions(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "mut_revision_race")
	entered, release := make(chan struct{}, 2), make(chan struct{})
	h := &mutationProbe{run: func(ctx context.Context) ([]byte, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return mutationOutput(0), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	results := make(chan error, 2)
	for _, key := range []string{"revision-race-001", "revision-race-002"} {
		go func(key string) {
			_, err := api.ExecuteExtensionCommand(f.owner, mutationRequest(key, 0))
			results <- err
		}(key)
	}
	waitMutationSignal(t, f.ctx, entered)
	waitMutationSignal(t, f.ctx, entered)
	close(release)
	a, b := <-results, <-results
	if !((a == nil && status.Code(b) == codes.Aborted) || (b == nil && status.Code(a) == codes.Aborted)) {
		t.Fatal(a, b)
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}

func TestExtensionP1MutationCancellationReleasesKey(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "mut_cancel")
	entered := make(chan struct{})
	h := new(mutationProbe)
	h.run = func(ctx context.Context) ([]byte, error) {
		if h.calls.Load() == 1 {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return mutationOutput(0), nil
	}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	ctx, cancel := context.WithCancel(f.owner)
	defer cancel()
	done := make(chan error, 1)
	r := mutationRequest("cancel-command-001", 0)
	go func() { _, err := api.ExecuteExtensionCommand(ctx, r); done <- err }()
	waitMutationSignal(t, f.ctx, entered)
	cancel()
	requireCode(t, <-done, codes.Canceled)
	result, err := api.ExecuteExtensionCommand(f.owner, r)
	if err != nil || result.GetReplayed() || h.calls.Load() != 2 {
		t.Fatal(result, err, h.calls.Load())
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}

func TestExtensionP1MutationReceiptAndOutboxQuota(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "mut_log_quota")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	_, err := f.store.pool.Exec(f.ctx, `INSERT INTO extension_p1_receipts SELECT 'install','demo','quota-principal','acknowledge',n::text,decode(repeat('00',32),'hex'),''::bytea,clock_timestamp() FROM generate_series(1,1024) n`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.ExecuteExtensionCommand(f.owner, mutationRequest("receipt-quota-001", 0))
	requireCode(t, err, codes.ResourceExhausted)
	mutationCounts(t, f, 0, 0, 1024, 0)
	_, err = f.store.pool.Exec(f.ctx, `DELETE FROM extension_p1_receipts; INSERT INTO extension_p1_outbox SELECT 'install','demo',0,n,'event_'||n,'note_changed','{}'::bytea FROM generate_series(1,4096) n`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.ExecuteExtensionCommand(f.owner, mutationRequest("outbox-quota-001", 0))
	requireCode(t, err, codes.ResourceExhausted)
	mutationCounts(t, f, 0, 0, 0, 4096)
}

func waitMutationSignal(t *testing.T, ctx context.Context, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal("Не достигнут барьер вызова", ctx.Err())
	}
}
