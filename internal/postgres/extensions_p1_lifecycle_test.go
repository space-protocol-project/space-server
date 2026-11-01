package postgres

import (
	"context"
	"crypto/sha256"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func lifecycleToken(ctx context.Context) string {
	return strings.TrimPrefix(outgoingAuthorization(ctx), "Bearer ")
}
func managedFixture(t *testing.T, f *channelFixture, h extensions.Runtime) (*ExtensionP1ManagedService, pb.ExtensionP1ServiceClient) {
	t.Helper()
	c := f.create(t, "managed")
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, mutationBindings(c.Id, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	return s, extensionP1RPCClient(t, f, s)
}
func managedChange(t *testing.T, f *channelFixture, s *ExtensionP1ManagedService, action string, revision int64) int64 {
	t.Helper()
	d, err := s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", action, revision)
	if err != nil {
		t.Fatal(err)
	}
	return d.Revision
}
func lifecycleAuditCount(t *testing.T, f *channelFixture, want int) {
	t.Helper()
	var n int
	if err := f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM admin_audit WHERE action LIKE 'extension.p1.lifecycle/%'").Scan(&n); err != nil || n != want {
		t.Fatal(n, err)
	}
}

func TestExtensionP1LifecyclePermissions(t *testing.T) {
	f := newChannelFixture(t)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	s, api := managedFixture(t, f, h)
	_, err := api.QueryExtension(f.owner, extensionP1Query())
	requireCode(t, err, codes.FailedPrecondition)
	_, err = s.Change(f.ctx, "invalid", s.Generation(), "install", "enable", 1)
	requireCode(t, err, codes.Unauthenticated)
	member, _ := f.enroll(t, true)
	_, err = s.Change(f.ctx, lifecycleToken(member), s.Generation(), "install", "enable", 1)
	requireCode(t, err, codes.PermissionDenied)
	_, err = s.Change(f.ctx, lifecycleToken(f.owner), "old-generation", "install", "enable", 1)
	requireCode(t, err, codes.FailedPrecondition)
	managedChange(t, f, s, "enable", 1)
	var recorded string
	if err = f.store.pool.QueryRow(f.ctx, "SELECT action FROM admin_audit WHERE action LIKE 'extension.p1.lifecycle/%'").Scan(&recorded); err != nil || recorded != "extension.p1.lifecycle/"+s.Generation()+"/install/enable" {
		t.Fatal("Аудит не привязан к поколению", recorded, err)
	}
	_, err = s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", "disable", 1)
	requireCode(t, err, codes.Aborted)
	_, err = s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", "begin_upgrade", 2)
	requireCode(t, err, codes.FailedPrecondition)
	_, err = s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", "quarantine", 2)
	requireCode(t, err, codes.FailedPrecondition)
	if _, err = api.QueryExtension(f.owner, extensionP1Query()); err != nil {
		t.Fatal(err)
	}
	_, err = f.auth.Logout(f.owner, &pb.LogoutRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", "disable", 2)
	requireCode(t, err, codes.Unauthenticated)
	lifecycleAuditCount(t, f, 1)
}

func TestExtensionP1LifecycleDisabledReceipt(t *testing.T) {
	f := newChannelFixture(t)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	s, api := managedFixture(t, f, h)
	managedChange(t, f, s, "enable", 1)
	r := mutationRequest("lifecycle-receipt-001", 0)
	if _, err := api.ExecuteExtensionCommand(f.owner, r); err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "disable", 2)
	if _, err := s.SystemTransition(f.ctx, "install", "finish_disable", 3); err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "remove", 4)
	_, err := api.ExecuteExtensionCommand(f.owner, r)
	requireCode(t, err, codes.FailedPrecondition)
	result, err := api.ReconcileExtensionCommand(f.owner, &pb.ReconcileExtensionCommandRequest{Command: r})
	if err != nil || result.GetOutcome() != pb.ExtensionCommandOutcome_EXTENSION_COMMAND_OUTCOME_CONFIRMED {
		t.Fatal(result, err)
	}
	verifyExtensionCrashReceipt(t, f, result.Receipt)
	mutationCounts(t, f, 1, 1, 1, 1)
	if h.calls.Load() != 1 {
		t.Fatal(h.calls.Load())
	}
	lifecycleAuditCount(t, f, 3)
}

func TestExtensionP1LifecycleCancelActive(t *testing.T) {
	for _, kind := range []string{"query", "mutation", "quarantine"} {
		t.Run(kind, func(t *testing.T) {
			f := newChannelFixture(t)
			entered, exit := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(exit) }) })
			h := &mutationProbe{run: func(ctx context.Context) ([]byte, error) {
				close(entered)
				<-ctx.Done()
				<-exit
				return mutationOutput(0), nil
			}}
			s, api := managedFixture(t, f, h)
			managedChange(t, f, s, "enable", 1)
			done := make(chan error, 1)
			go func() {
				var err error
				if kind == "query" {
					_, err = api.QueryExtension(f.owner, extensionP1Query())
				} else {
					_, err = api.ExecuteExtensionCommand(f.owner, mutationRequest("lifecycle-active-001", 0))
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("Нет активного вызова")
			}
			if kind == "quarantine" {
				if _, err := s.SystemTransition(f.ctx, "install", "quarantine", 2); err != nil {
					t.Fatal(err)
				}
			} else {
				managedChange(t, f, s, "disable", 2)
				_, err := s.SystemTransition(f.ctx, "install", "finish_disable", 3)
				requireCode(t, err, codes.FailedPrecondition)
			}
			_, err := api.QueryExtension(f.owner, extensionP1Query())
			requireCode(t, err, codes.FailedPrecondition)
			once.Do(func() { close(exit) })
			select {
			case err = <-done:
				requireCode(t, err, codes.Canceled)
			case <-time.After(3 * time.Second):
				t.Fatal("Вызов не завершился")
			}
			if kind != "quarantine" {
				if _, err = s.SystemTransition(f.ctx, "install", "finish_disable", 3); err != nil {
					t.Fatal(err)
				}
			}
			mutationCounts(t, f, 0, 0, 0, 0)
		})
	}
}

func TestExtensionP1LifecycleCommitFence(t *testing.T) {
	f := newChannelFixture(t)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	s, api := managedFixture(t, f, h)
	managedChange(t, f, s, "enable", 1)
	release := extensionCommitBarrier(t, f)
	done := make(chan error, 1)
	go func() {
		_, err := api.ExecuteExtensionCommand(f.owner, mutationRequest("lifecycle-commit-001", 0))
		done <- err
	}()
	waitExtensionSQLLock(t, f, "INSERT INTO extension_p1_outbox")
	ctx, cancel := context.WithTimeout(f.ctx, 100*time.Millisecond)
	_, err := s.Change(ctx, lifecycleToken(f.owner), s.Generation(), "install", "disable", 2)
	cancel()
	requireCode(t, err, codes.DeadlineExceeded)
	lifecycleAuditCount(t, f, 1)
	release()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "disable", 2)
	mutationCounts(t, f, 1, 1, 1, 1)
}

func TestExtensionP1LifecycleAuditRollback(t *testing.T) {
	f := newChannelFixture(t)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	s, api := managedFixture(t, f, h)
	managedChange(t, f, s, "enable", 1)
	_, err := f.store.pool.Exec(f.ctx, `CREATE FUNCTION reject_lifecycle() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'fixture'; END$$;
CREATE TRIGGER reject_lifecycle BEFORE INSERT ON admin_audit FOR EACH ROW EXECUTE FUNCTION reject_lifecycle()`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", "disable", 2)
	requireCode(t, err, codes.Internal)
	if _, err = api.QueryExtension(f.owner, extensionP1Query()); err != nil {
		t.Fatal(err)
	}
	lifecycleAuditCount(t, f, 1)
}

func TestExtensionP1LifecycleRevocationAtBoundary(t *testing.T) {
	for _, kind := range []string{"session", "owner", "scope"} {
		t.Run(kind, func(t *testing.T) {
			f := newChannelFixture(t)
			h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
			s, _ := managedFixture(t, f, h)
			tx, err := f.store.pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx)
			hash := sha256.Sum256([]byte(lifecycleToken(f.owner)))
			fragment := "SELECT id FROM device_grants"
			if kind == "owner" {
				_, err = tx.Exec(f.ctx, "UPDATE space_settings SET owner_id=NULL,revision=revision+1 WHERE singleton=true")
				fragment = "SELECT singleton FROM space_settings"
			} else {
				_, err = tx.Exec(f.ctx, "SELECT id FROM device_grants WHERE id=(SELECT grant_id FROM auth_sessions WHERE token_hash=$1) FOR UPDATE", hash[:])
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, e := s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", "enable", 1)
				done <- e
			}()
			waitExtensionSQLLock(t, f, fragment)
			switch kind {
			case "session":
				_, err = f.auth.Logout(f.owner, &pb.LogoutRequest{})
			case "scope":
				_, err = tx.Exec(f.ctx, "UPDATE device_grants SET scopes=ARRAY['chat.read','chat.write']::text[] WHERE id=(SELECT grant_id FROM auth_sessions WHERE token_hash=$1)", hash[:])
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.Unauthenticated {
				t.Fatal(err)
			}
			lifecycleAuditCount(t, f, 0)
		})
	}
}

func TestExtensionP1LifecycleNewGeneration(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "generation")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	bindings := mutationBindings(c.Id, []byte{1})
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	_, err = f.store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	requireCode(t, err, codes.FailedPrecondition)
	f.store.Close()
	store, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	next, err := store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation() == s.Generation() {
		t.Fatal("Поколение повторилось")
	}
	_, err = next.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "install", "enable", 1)
	requireCode(t, err, codes.FailedPrecondition)
	d := next.control.installations["install"].coordinator.Preview(lifecycleObservation("enable", "owner", 1))
	if d.Code != "OK" || d.Revision != 2 {
		t.Fatal(d)
	}
}

func TestExtensionP1LifecycleInstallationScope(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "install_scope")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	bindings := mutationBindings(c.Id, []byte{1})
	bindings["sibling"] = bindings["demo"]
	other := bindings["demo"]
	other.Installation = "other_install"
	bindings["other"] = other
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	api := extensionP1RPCClient(t, f, s)
	managedChange(t, f, s, "enable", 1)
	if _, err = s.Change(f.ctx, lifecycleToken(f.owner), s.Generation(), "other_install", "enable", 1); err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "disable", 2)
	for _, id := range []string{"demo", "sibling"} {
		r := extensionP1Query()
		r.InstanceId = id
		_, err = api.QueryExtension(f.owner, r)
		requireCode(t, err, codes.FailedPrecondition)
	}
	r := extensionP1Query()
	r.InstanceId = "other"
	if _, err = api.QueryExtension(f.owner, r); err != nil {
		t.Fatal(err)
	}
	if h.calls.Load() != 1 {
		t.Fatal(h.calls.Load())
	}
}

func TestExtensionP1LifecycleMemberACL(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "managed_acl")
	a := extensionAccessChange(t, f, c, "acl")
	h := &mutationProbe{run: func(context.Context) ([]byte, error) {
		if err := a.revoke(); err != nil {
			return nil, err
		}
		return mutationOutput(0), nil
	}}
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, mutationBindings(c.Id, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	api := extensionP1RPCClient(t, f, s)
	_, err = api.ExecuteExtensionCommand(a.member, mutationRequest("lifecycle-acl-001", 0))
	requireCode(t, err, codes.PermissionDenied)
	mutationCounts(t, f, 0, 0, 0, 0)
	managedChange(t, f, s, "disable", 2)
}

func TestExtensionP1LifecycleRealWASM(t *testing.T) {
	worker := os.Getenv("SPACE_P1_WORKER")
	if worker == "" {
		t.Skip("Нужен изолированный CI")
	}
	f := newChannelFixture(t)
	c := f.create(t, "managed_wasm")
	host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), worker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	module, err := os.ReadFile(os.Getenv("SPACE_P1_MUTATION_MODULE"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, host, mutationBindings(c.Id, module))
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	api := extensionP1RPCClient(t, f, s)
	r := mutationRequest("lifecycle-real-001", 0)
	if _, err = api.ExecuteExtensionCommand(f.owner, r); err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "disable", 2)
	_, err = api.ExecuteExtensionCommand(f.owner, r)
	requireCode(t, err, codes.FailedPrecondition)
	response, err := api.ReconcileExtensionCommand(f.owner, &pb.ReconcileExtensionCommandRequest{Command: r})
	if err != nil || response.GetOutcome() != pb.ExtensionCommandOutcome_EXTENSION_COMMAND_OUTCOME_CONFIRMED {
		t.Fatal(response, err)
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}
