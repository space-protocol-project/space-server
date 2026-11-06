package extensions

import (
	"errors"
	"math"
	"sync"
	"testing"
)

func TestDeliveryGeneration(t *testing.T) {
	var bodies []string
	write := func(b []byte) error { bodies = append(bodies, string(b)); return nil }
	close := func() error { return nil }
	g := newDeliveryGrant(write, close)
	body := []byte("ready")
	call, err := g.begin()
	if err != nil {
		t.Fatal(err)
	}
	old, err := g.prepare(call, body)
	if err != nil {
		t.Fatal(err)
	}
	body[0] = 'X'
	other := newDeliveryGrant(write, close)
	if !errors.Is(other.deliver(old), errDeliveryRevoked) {
		t.Fatal("Чужое разрешение принято")
	}
	if err := g.deliver(old); err != nil {
		t.Fatal(err)
	}
	if err := g.revoke(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.prepare(call, body); !errors.Is(err, errDeliveryRevoked) {
		t.Fatal(err)
	}
	if err := g.renew(write, close); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(g.deliver(old), errDeliveryRevoked) {
		t.Fatal("Старое поколение ожило")
	}
	freshCall, err := g.begin()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := g.prepare(freshCall, []byte("fresh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.deliver(fresh); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != "ready" || bodies[1] != "fresh" {
		t.Fatal(bodies)
	}
}

func TestDeliveryRevokeFailure(t *testing.T) {
	failure := errors.New("transport failure")
	write := func([]byte) error { return nil }
	g := newDeliveryGrant(write, func() error { return failure })
	call, _ := g.begin()
	p, _ := g.prepare(call, []byte("ready"))
	if !errors.Is(g.revoke(), failure) {
		t.Fatal("Нет ошибки отключения")
	}
	if !errors.Is(g.deliver(p), errDeliveryRevoked) {
		t.Fatal("Ответ после отказа отзыва")
	}
	if !errors.Is(g.renew(write, func() error { return nil }), errDeliveryRevoked) {
		t.Fatal("Новое поколение до отключения")
	}
	g.disconnect = func() error { return nil }
	if err := g.revoke(); err != nil {
		t.Fatal(err)
	}
	g.generation = math.MaxUint64
	if !errors.Is(g.renew(write, g.disconnect), errDeliveryRevoked) {
		t.Fatal("Переполнение поколения")
	}
}

func TestDeliveryWriteFailure(t *testing.T) {
	g := newDeliveryGrant(func([]byte) error { return errors.New("partial write") }, func() error { return nil })
	call, _ := g.begin()
	p, _ := g.prepare(call, []byte("ready"))
	if g.deliver(p) == nil {
		t.Fatal("Нет ошибки записи")
	}
	if !errors.Is(g.deliver(p), errDeliveryRevoked) {
		t.Fatal("Повтор после неоднозначной записи")
	}
}

func TestDeliveryConcurrentRevoke(t *testing.T) {
	for i := 0; i < 100; i++ {
		var mu sync.Mutex
		var events []string
		appendEvent := func(event string) { mu.Lock(); defer mu.Unlock(); events = append(events, event) }
		g := newDeliveryGrant(func([]byte) error { appendEvent("write"); return nil }, func() error { appendEvent("revoke"); return nil })
		call, _ := g.begin()
		p, _ := g.prepare(call, []byte("ready"))
		start := make(chan struct{})
		written, revoked := make(chan error, 1), make(chan error, 1)
		go func() { <-start; written <- g.deliver(p) }()
		go func() { <-start; revoked <- g.revoke() }()
		close(start)
		writeErr, revokeErr := <-written, <-revoked
		if revokeErr != nil {
			t.Fatal(revokeErr)
		}
		if writeErr == nil {
			if len(events) != 2 || events[0] != "write" || events[1] != "revoke" {
				t.Fatal(events)
			}
		} else if !errors.Is(writeErr, errDeliveryRevoked) || len(events) != 1 || events[0] != "revoke" {
			t.Fatal(writeErr, events)
		}
		if !errors.Is(g.deliver(p), errDeliveryRevoked) {
			t.Fatal("Запись после завершения отзыва")
		}
	}
}
