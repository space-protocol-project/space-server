//go:build linux

package postgres

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

func TestExtensionP1LifecycleProcessRestart(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "mut_crash")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, mutationBindings(c.Id, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	dir := t.TempDir()
	calls, committed := filepath.Join(dir, "calls"), filepath.Join(dir, "committed")
	p := startExtensionCrashProcess(t, f, t.TempDir(), calls, committed, true, "SPACE_P1_CRASH_MANAGED=yes")
	conn, api := extensionCrashClient(t, p.address)
	ctx, cancel := context.WithTimeout(f.owner, 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	r := mutationRequest("registry-process-001", 0)
	go func() { _, e := api.ExecuteExtensionCommand(ctx, r); done <- e }()
	waitExtensionCrashFile(t, committed)
	p.kill(t)
	conn.Close()
	<-done
	lifecycleRegistryState(t, f, "enabled", 2)
	mutationCounts(t, f, 1, 1, 1, 1)
	next := startExtensionCrashProcess(t, f, t.TempDir(), calls, committed, false, "SPACE_P1_CRASH_MANAGED=yes")
	_, api = extensionCrashClient(t, next.address)
	response, err := api.ReconcileExtensionCommand(f.owner, &pb.ReconcileExtensionCommandRequest{Command: r})
	if err != nil || response.GetOutcome() != pb.ExtensionCommandOutcome_EXTENSION_COMMAND_OUTCOME_CONFIRMED {
		t.Fatal(response, err)
	}
	lifecycleRegistryState(t, f, "enabled", 2)
	mutationCounts(t, f, 1, 1, 1, 1)
}

func TestExtensionP1LifecycleRestartWaitsForCommit(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "restart_commit")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	bindings := mutationBindings(c.Id, []byte{1})
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	api := extensionP1RPCClient(t, f, s)
	release := extensionCommitBarrier(t, f)
	done := make(chan error, 1)
	go func() {
		_, e := api.ExecuteExtensionCommand(f.owner, mutationRequest("restart-commit-001", 0))
		done <- e
	}()
	waitExtensionSQLLock(t, f, "INSERT INTO extension_p1_outbox")
	store, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(f.ctx, 100*time.Millisecond)
	_, err = store.NewExtensionP1ManagedService(ctx, h, bindings)
	cancel()
	if err == nil {
		t.Fatal("Новое поколение пересекло COMMIT")
	}
	release()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if _, err = store.NewExtensionP1ManagedService(f.ctx, h, bindings); err != nil {
		t.Fatal(err)
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}
