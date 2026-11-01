package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/space-protocol-project/space-server/experiments/extensions/lifecycle"
	"google.golang.org/grpc/codes"
)

func lifecycleRegistryState(t *testing.T, f *channelFixture, state string, revision int64) {
	t.Helper()
	var got string
	var rev int64
	err := f.store.pool.QueryRow(f.ctx, `SELECT state,revision FROM extension_p1_installations WHERE installation='install'`).Scan(&got, &rev)
	if err != nil || got != state || rev != revision {
		t.Fatal(got, rev, err)
	}
}

func TestExtensionP1LifecyclePersistentStates(t *testing.T) {
	for _, state := range []string{"disabled", "enabled", "disabling", "quarantined", "removed"} {
		t.Run(state, func(t *testing.T) {
			f := newChannelFixture(t)
			c := f.create(t, "persist")
			h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
			bindings := mutationBindings(c.Id, []byte{1})
			s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, bindings)
			if err != nil {
				t.Fatal(err)
			}
			rev := int64(1)
			switch state {
			case "enabled":
				rev = managedChange(t, f, s, "enable", 1)
			case "disabling":
				managedChange(t, f, s, "enable", 1)
				rev = managedChange(t, f, s, "disable", 2)
			case "removed":
				rev = managedChange(t, f, s, "remove", 1)
			case "quarantined":
				var d lifecycle.Decision
				d, err = s.SystemTransition(f.ctx, "install", "quarantine", 1)
				if err != nil {
					t.Fatal(err)
				}
				rev = d.Revision
			}
			lifecycleRegistryState(t, f, state, rev)
			f.store.Close()
			f.store, _, err = Open(f.ctx, f.url)
			if err != nil {
				t.Fatal(err)
			}
			defer f.store.Close()
			next, err := f.store.NewExtensionP1ManagedService(f.ctx, h, bindings)
			if err != nil {
				t.Fatal(err)
			}
			d := next.control.installations["install"].coordinator.Preview(lifecycleObservation("invalid", "system", rev))
			if d.State != state || d.Revision != rev {
				t.Fatal(d)
			}
			if state == "disabling" {
				if _, err = next.SystemTransition(f.ctx, "install", "finish_disable", rev); err != nil {
					t.Fatal(err)
				}
				lifecycleRegistryState(t, f, "disabled", rev+1)
			}
		})
	}
}

func TestExtensionP1LifecycleGenerationFencesActiveMutation(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "fence_old")
	entered, exit := make(chan struct{}), make(chan struct{})
	h := &mutationProbe{run: func(ctx context.Context) ([]byte, error) {
		close(entered)
		select {
		case <-exit:
			return mutationOutput(0), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	bindings := mutationBindings(c.Id, []byte{1})
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	api := extensionP1RPCClient(t, f, s)
	done := make(chan error, 1)
	go func() {
		_, e := api.ExecuteExtensionCommand(f.owner, mutationRequest("registry-fenced-001", 0))
		done <- e
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Вызов не начался")
	}
	store, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	next, err := store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	close(exit)
	if err != nil {
		t.Fatal(err)
	}
	requireCode(t, <-done, codes.FailedPrecondition)
	mutationCounts(t, f, 0, 0, 0, 0)
	_, err = s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", "disable", 2)
	requireCode(t, err, codes.FailedPrecondition)
	managedChange(t, f, next, "disable", 2)
	lifecycleRegistryState(t, f, "disabling", 3)
}

func TestExtensionP1LifecycleRegistryRollbackAndBackup(t *testing.T) {
	f := newChannelFixture(t)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	s, _ := managedFixture(t, f, h)
	managedChange(t, f, s, "enable", 1)
	if err := CheckExtensionP1Backup(f.ctx, f.url, false); err != nil {
		t.Fatal(err)
	}
	if err := CheckExtensionP1Backup(f.ctx, f.url, true); err != nil {
		t.Fatal(err)
	}
	lifecycleRegistryState(t, f, "enabled", 2)
	_, err := s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", "disable", 2)
	requireCode(t, err, codes.FailedPrecondition)
	if _, err = f.store.pool.Exec(f.ctx, `DROP TABLE extension_p1_installations CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err = CheckExtensionP1Backup(f.ctx, f.url, true); err == nil {
		t.Fatal("Принята копия без реестра")
	}
}

func TestExtensionP1LifecycleBindingSetPinned(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "binding_set")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	bindings := mutationBindings(c.Id, []byte{1})
	bindings["sibling"] = bindings["demo"]
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	store, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	delete(bindings, "sibling")
	_, err = store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	requireCode(t, err, codes.FailedPrecondition)
	if err = s.control.checkGeneration(f.ctx, "install"); err != nil {
		t.Fatal("Неудачный запуск сменил поколение", err)
	}
}
