package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
)

func newExtensionOutboxFixture(t *testing.T) (*channelFixture, *ExtensionP1Outbox) {
	t.Helper()
	f := newChannelFixture(t)
	c := f.create(t, "outbox")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(mutationOutput(0), &envelope); err != nil {
			return nil, err
		}
		plan := new(pb.ExtensionMutationPlan)
		if err := protojson.Unmarshal(envelope["plan"], plan); err != nil {
			return nil, err
		}
		plan.Events = append(plan.Events, &pb.ExtensionMutationEvent{Type: "note_changed", PayloadJson: []byte(`{"second":true}`)})
		raw, err := protojson.Marshal(plan)
		if err != nil {
			return nil, err
		}
		envelope["plan"] = raw
		return json.Marshal(envelope)
	}}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	if _, err := api.ExecuteExtensionCommand(f.owner, mutationRequest("outbox-command-001", 0)); err != nil {
		t.Fatal(err)
	}
	outbox, err := f.store.NewExtensionP1Outbox(f.ctx, "install", "demo", "projection")
	if err != nil {
		t.Fatal(err)
	}
	return f, outbox
}

func claimExtensionEvent(t *testing.T, o *ExtensionP1Outbox) *ExtensionP1Delivery {
	t.Helper()
	e, err := o.Claim(context.Background())
	if err != nil || e == nil {
		t.Fatal(e, err)
	}
	return e
}

func expireExtensionDelivery(t *testing.T, f *channelFixture) {
	t.Helper()
	if _, err := f.store.pool.Exec(f.ctx, `UPDATE extension_p1_delivery SET lease_until=clock_timestamp()-interval '1 second' WHERE installation='install' AND instance='demo'`); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionP1OutboxLeaseOrderAndAck(t *testing.T) {
	f, o := newExtensionOutboxFixture(t)
	first := claimExtensionEvent(t, o)
	if first.Installation != "install" || first.Instance != "demo" || first.Revision != 1 || first.Ordinal != 0 || first.Attempt != 1 || string(first.Payload) != "{}" || first.Type != "note_changed" {
		t.Fatal(first)
	}
	if e, err := o.Claim(f.ctx); err != nil || e != nil {
		t.Fatal("Аренда выдана дважды", e, err)
	}
	requireCode(t, o.Ack(f.ctx, strings.Repeat("0", 64)), codes.FailedPrecondition)
	expireExtensionDelivery(t, f)
	requireCode(t, o.Ack(f.ctx, first.Token), codes.FailedPrecondition)
	second := claimExtensionEvent(t, o)
	if second.ID != first.ID || second.Token == first.Token || second.Attempt != 2 {
		t.Fatal(first, second)
	}
	requireCode(t, o.Ack(f.ctx, first.Token), codes.FailedPrecondition)
	if err := o.Ack(f.ctx, second.Token); err != nil {
		t.Fatal(err)
	}
	if err := o.Ack(f.ctx, second.Token); err != nil {
		t.Fatal("Повтор ACK", err)
	}
	next := claimExtensionEvent(t, o)
	if next.Ordinal != 1 || next.ID == first.ID || next.Attempt != 1 || string(next.Payload) != `{"second":true}` {
		t.Fatal(next)
	}
	if err := o.Ack(f.ctx, second.Token); err != nil {
		t.Fatal(err)
	}
	if e, err := o.Claim(f.ctx); err != nil || e != nil {
		t.Fatal("Старый ACK подтвердил новое событие", e, err)
	}
	if err := o.Ack(f.ctx, next.Token); err != nil {
		t.Fatal(err)
	}
	if e, err := o.Claim(f.ctx); err != nil || e != nil {
		t.Fatal(e, err)
	}
	mutationCounts(t, f, 1, 1, 1, 2)
}

func TestExtensionP1OutboxConcurrentStores(t *testing.T) {
	f, first := newExtensionOutboxFixture(t)
	store, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	second, err := store.NewExtensionP1Outbox(f.ctx, "install", "demo", "projection")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan *ExtensionP1Delivery, 2)
	errors := make(chan error, 2)
	for _, o := range []*ExtensionP1Outbox{first, second} {
		wg.Add(1)
		go func() { defer wg.Done(); e, err := o.Claim(f.ctx); results <- e; errors <- err }()
	}
	wg.Wait()
	close(results)
	close(errors)
	count := 0
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for e := range results {
		if e != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatal("Одновременно выданных аренд", count)
	}
	_, err = store.NewExtensionP1Outbox(f.ctx, "install", "demo", "foreign")
	requireCode(t, err, codes.FailedPrecondition)
}

func TestExtensionP1OutboxRestartAndIdempotentReceiver(t *testing.T) {
	f, o := newExtensionOutboxFixture(t)
	if _, err := f.store.pool.Exec(f.ctx, `CREATE TABLE p1_test_inbox(event_id text PRIMARY KEY,payload bytea NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	first := claimExtensionEvent(t, o)
	// Эффект получателя зафиксирован, а ACK отправителя потерян.
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO p1_test_inbox VALUES($1,$2)`, first.ID, first.Payload); err != nil {
		t.Fatal(err)
	}
	store, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopened, err := store.NewExtensionP1Outbox(f.ctx, "install", "demo", "projection")
	if err != nil {
		t.Fatal(err)
	}
	if e, err := reopened.Claim(f.ctx); err != nil || e != nil {
		t.Fatal("Перезапуск потерял аренду", e, err)
	}
	expireExtensionDelivery(t, f)
	delivered, err := reopened.DispatchOne(f.ctx, func(ctx context.Context, e ExtensionP1Delivery) error {
		if e.ID != first.ID || e.Attempt != 2 {
			return errors.New("повтор изменил событие")
		}
		_, err := store.pool.Exec(ctx, `INSERT INTO p1_test_inbox VALUES($1,$2) ON CONFLICT DO NOTHING`, e.ID, e.Payload)
		return err
	})
	if err != nil || !delivered {
		t.Fatal(delivered, err)
	}
	var count int
	if err = f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM p1_test_inbox`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if next := claimExtensionEvent(t, reopened); next.Ordinal != 1 {
		t.Fatal(next)
	}
}

func TestExtensionP1OutboxHandlerFailureAndCancellation(t *testing.T) {
	f, o := newExtensionOutboxFixture(t)
	failure := errors.New("receiver unavailable")
	delivered, err := o.DispatchOne(f.ctx, func(context.Context, ExtensionP1Delivery) error { return failure })
	if !delivered || !errors.Is(err, failure) {
		t.Fatal(delivered, err)
	}
	if e, err := o.Claim(f.ctx); err != nil || e != nil {
		t.Fatal(e, err)
	}
	expireExtensionDelivery(t, f)
	ctx, cancel := context.WithCancel(f.ctx)
	delivered, err = o.DispatchOne(ctx, func(context.Context, ExtensionP1Delivery) error { cancel(); return nil })
	if !delivered || err == nil {
		t.Fatal("Отмена подтвердила событие", delivered, err)
	}
	expireExtensionDelivery(t, f)
	if next := claimExtensionEvent(t, o); next.Ordinal != 0 || next.Attempt != 3 {
		t.Fatal(next)
	}
	_, err = o.DispatchOne(f.ctx, nil)
	requireCode(t, err, codes.InvalidArgument)
}

func TestExtensionP1OutboxBindingIsolation(t *testing.T) {
	f, o := newExtensionOutboxFixture(t)
	// Вторая доверенная привязка и событие: проверяем границу экземпляров доставки.
	_, err := f.store.pool.Exec(f.ctx, `INSERT INTO extension_p1_instances SELECT installation,'other',channel,package_hash,revision FROM extension_p1_instances WHERE instance='demo';
 INSERT INTO extension_p1_outbox SELECT installation,'other',revision,ordinal,'other-event',type,payload FROM extension_p1_outbox WHERE instance='demo' AND ordinal=0`)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.store.NewExtensionP1Outbox(f.ctx, "install", "other", "projection")
	if err != nil {
		t.Fatal(err)
	}
	one, two := claimExtensionEvent(t, o), claimExtensionEvent(t, other)
	if one.Instance == two.Instance || one.ID == two.ID {
		t.Fatal(one, two)
	}
	requireCode(t, other.Ack(f.ctx, one.Token), codes.FailedPrecondition)
	if err = other.Ack(f.ctx, two.Token); err != nil {
		t.Fatal(err)
	}
	if e, err := o.Claim(f.ctx); err != nil || e != nil {
		t.Fatal("Чужой ACK изменил аренду", e, err)
	}
}

func TestExtensionP1OutboxAckExpiryWhileLocked(t *testing.T) {
	f, o := newExtensionOutboxFixture(t)
	e := claimExtensionEvent(t, o)
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err = tx.Exec(f.ctx, `UPDATE extension_p1_delivery SET lease_until=clock_timestamp()+interval '100 milliseconds'`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- o.Ack(f.ctx, e.Token) }()
	waitExtensionSQLLock(t, f, "SELECT lease_token,last_ack_token")
	time.Sleep(150 * time.Millisecond)
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	requireCode(t, <-done, codes.FailedPrecondition)
	if again := claimExtensionEvent(t, o); again.ID != e.ID || again.Attempt != 2 {
		t.Fatal(again)
	}
}
