//go:build linux

package postgres

import (
	"context"
	"net/url"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
)

func TestExtensionP1LifecycleUncertainAuditCommit(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "lifecycle_commit")
	proxy, database := newExtensionDatabaseProxy(t, f.url, "lifecycle_after_commit")
	address, err := url.Parse(database)
	if err != nil {
		t.Fatal(err)
	}
	options := address.Query()
	// Повторный INSERT аудита иначе использует уже подготовленный statement:
	// в Bind нет SQL-текста, по которому посредник выбирает транзакцию.
	options.Set("default_query_exec_mode", "simple_protocol")
	address.RawQuery = options.Encode()
	store, _, err := Open(f.ctx, address.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	s, err := store.NewExtensionP1ManagedService(f.ctx, h, mutationBindings(c.Id, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	proxy.armed.Store(true)
	_, err = s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", "disable", 2)
	requireCode(t, err, codes.Internal)
	select {
	case evidence := <-proxy.hit:
		t.Log(evidence)
	case <-time.After(time.Second):
		t.Fatal("Не перехвачен COMMIT аудита")
	}
	lifecycleAuditCount(t, f, 2)
	lifecycleRegistryState(t, f, "disabling", 3)
	api := extensionP1RPCClient(t, f, s)
	_, err = api.QueryExtension(f.owner, extensionP1Query())
	requireCode(t, err, codes.Unavailable)
	_, err = s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", "enable", 3)
	requireCode(t, err, codes.Unavailable)
	next, err := f.store.NewExtensionP1ManagedService(f.ctx, h, mutationBindings(c.Id, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = next.SystemTransition(f.ctx, "install", "finish_disable", 3); err != nil {
		t.Fatal(err)
	}
	lifecycleRegistryState(t, f, "disabled", 4)
	if h.calls.Load() != 0 {
		t.Fatal("Неопределённый COMMIT разрешил WASM")
	}
}
