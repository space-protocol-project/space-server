package extensions

import (
	"errors"
	"testing"
	"time"
)

func deliveryResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Операция не завершилась")
		return nil
	}
}

func TestDeliveryCallOutlivesGeneration(t *testing.T) {
	var bodies []string
	write := func(body []byte) error { bodies = append(bodies, string(body)); return nil }
	disconnect := func() error { return nil }
	g := newDeliveryGrant(write, disconnect)
	started, resume := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- g.invoke(func() ([]byte, error) {
			close(started)
			<-resume
			return []byte("old-result"), nil
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(resume)
		t.Fatal("Вызов не начался")
	}
	changed := make(chan error, 1)
	go func() {
		if err := g.revoke(); err != nil {
			changed <- err
			return
		}
		changed <- g.renew(write, disconnect)
	}()
	// Новое поколение должно появиться, пока старое вычисление приостановлено.
	err := deliveryResult(t, changed)
	close(resume)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(deliveryResult(t, done), errDeliveryRevoked) {
		t.Fatal("Старый результат принят")
	}
	if len(bodies) != 0 {
		t.Fatal(bodies)
	}
	if err := g.invoke(func() ([]byte, error) { return []byte("fresh-result"), nil }); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0] != "fresh-result" {
		t.Fatal(bodies)
	}
}

func TestDeliveryCallBinding(t *testing.T) {
	write := func([]byte) error { return nil }
	disconnect := func() error { return nil }
	g, other := newDeliveryGrant(write, disconnect), newDeliveryGrant(write, disconnect)
	call, _ := g.begin()
	for _, invalid := range []deliveryCall{{}, call} {
		if _, err := other.prepare(invalid, []byte("result")); !errors.Is(err, errDeliveryRevoked) {
			t.Fatal("Чужой или пустой снимок принят")
		}
	}
	if _, err := g.prepare(call, make([]byte, 65537)); !errors.Is(err, errDeliveryRevoked) {
		t.Fatal("Превышен размер ответа")
	}
	if err := g.revoke(); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := g.invoke(func() ([]byte, error) { called = true; return nil, nil }); !errors.Is(err, errDeliveryRevoked) || called {
		t.Fatal("Вычисление началось без доступа")
	}
}

func TestDeliveryComputeFailure(t *testing.T) {
	writes := 0
	g := newDeliveryGrant(func([]byte) error { writes++; return nil }, func() error { return nil })
	failure := errors.New("compute failed")
	if err := g.invoke(func() ([]byte, error) { return []byte("partial"), failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatal("Отправлен результат неудачного вычисления")
	}
	if err := g.invoke(func() ([]byte, error) { return []byte("healthy"), nil }); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatal(writes)
	}
}
