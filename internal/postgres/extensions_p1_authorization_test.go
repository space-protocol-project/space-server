package postgres

import (
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
)

// Блокировка настоящей таблицы сессий после исполнения не является отзывом ACL.
func TestExtensionP1AuthorizationDeadline(t *testing.T) {
	f := newChannelFixture(t)
	channel := f.create(t, "authorization_timeout")
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	locked := make(chan error, 1)
	var once atomic.Bool
	host := &extensionP1Probe{during: func([]byte) {
		if once.CompareAndSwap(false, true) {
			_, err := tx.Exec(f.ctx, "LOCK TABLE auth_sessions IN ACCESS EXCLUSIVE MODE")
			locked <- err
		}
	}}
	api := extensionP1Client(t, f, host, []byte("fixture"), channel.Id)
	response, err := api.QueryExtension(f.owner, extensionP1Query())
	select {
	case lockErr := <-locked:
		if lockErr != nil {
			t.Fatal(lockErr)
		}
	default:
		t.Fatal("Проверка не дошла до удержания таблицы сессий", err)
	}
	requireCode(t, err, codes.DeadlineExceeded)
	if response != nil || host.calls.Load() != 1 {
		t.Fatal("Ответ после тайм-аута авторизации", response, host.calls.Load())
	}
	if err = tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = api.QueryExtension(f.owner, extensionP1Query()); err != nil {
		t.Fatal("Свежий запрос после освобождения блокировки", err)
	}
}
