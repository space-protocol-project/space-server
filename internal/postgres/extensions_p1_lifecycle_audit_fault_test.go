//go:build linux

package postgres

import (
	"context"
	"net/url"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
)

func TestExtensionP1LifecycleJournalUncertainSystemCommit(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "system_commit")
	proxy, database := newExtensionDatabaseProxy(t, f.url, "lifecycle_system_after_commit")
	address, err := url.Parse(database)
	if err != nil {
		t.Fatal(err)
	}
	options := address.Query()
	options.Set("default_query_exec_mode", "simple_protocol")
	address.RawQuery = options.Encode()
	store, _, err := Open(f.ctx, address.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	bindings := mutationBindings(c.Id, []byte{1})
	s, err := store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	proxy.armed.Store(true)
	_, err = s.SystemTransition(f.ctx, "install", "quarantine", 2)
	requireCode(t, err, codes.Internal)
	select {
	case evidence := <-proxy.hit:
		t.Log(evidence)
	case <-time.After(time.Second):
		t.Fatal("Не перехвачен системный COMMIT")
	}
	lifecycleRegistryState(t, f, "quarantined", 3)
	lifecycleJournalCount(t, f, 3)
	lifecycleAuditCount(t, f, 1)
	api := extensionP1RPCClient(t, f, s)
	_, err = api.QueryExtension(f.owner, extensionP1Query())
	requireCode(t, err, codes.Unavailable)
	before := lifecycleJournal(t, f)
	next, err := f.store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	_, err = next.SystemTransition(f.ctx, "install", "quarantine", 2)
	requireCode(t, err, codes.Aborted)
	if lifecycleJournal(t, f) != before {
		t.Fatal("Перезапуск повторил событие")
	}
	if err = CheckExtensionP1Backup(f.ctx, f.url, false); err != nil {
		t.Fatal(err)
	}
}
