//go:build linux

package postgres

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

func TestExtensionDatabaseFaults(t *testing.T) {
	if os.Getenv("SPACE_P1_DATABASE_CONTAINER") == "" {
		t.Skip("Нужна отдельная PostgreSQL CI с разрешённым аварийным перезапуском")
	}
	for _, mode := range []string{"before_commit", "after_commit", "database_restart", "committed_restart"} {
		t.Run(mode, func(t *testing.T) { testExtensionDatabaseFault(t, mode) })
	}
}

func testExtensionDatabaseFault(t *testing.T, mode string) {
	t.Helper()
	f := newChannelFixtureTimeout(t, 90*time.Second)
	c := f.create(t, "database_fault")
	proxy, database := newExtensionDatabaseProxy(t, f.url, mode)
	store, _, err := Open(f.ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	var invoked atomic.Bool
	host := &mutationProbe{run: func(context.Context) ([]byte, error) {
		if !invoked.Swap(true) {
			proxy.armed.Store(mode != "database_restart")
		}
		return mutationOutput(0), nil
	}}
	svc, err := store.NewExtensionP1MutationService(f.ctx, host, mutationBindings(c.Id, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	api := extensionP1RPCClient(t, f, svc)
	if mode == "database_restart" {
		extensionDatabaseBarrier(t, f)
	}
	request := mutationRequest("database-fault-001", 0)
	done := make(chan error, 1)
	go func() { _, err := api.ExecuteExtensionCommand(f.owner, request); done <- err }()
	if mode == "database_restart" {
		waitExtensionSQLLock(t, f, "INSERT INTO extension_p1_outbox")
		restartExtensionDatabase(t, f)
	} else {
		select {
		case evidence := <-proxy.hit:
			t.Log(evidence)
			if mode == "committed_restart" {
				restartExtensionDatabase(t, f)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Не достигнут COMMIT в протоколе PostgreSQL")
		}
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Оборванная запись ошибочно вернула успех")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Оборванный RPC не завершился")
	}
	// Новое соединение наблюдателя не зависит от оборванного пула executor.
	f.store.pool.Reset()
	if _, err := f.store.pool.Exec(f.ctx, "DROP TRIGGER IF EXISTS wait_commit ON extension_p1_outbox"); err != nil {
		t.Fatal(err)
	}
	want := 0
	if mode == "after_commit" || mode == "committed_restart" {
		want = 1
	}
	verifyExtensionDatabaseRecovery(t, f, api, host, want, mode)
}

func verifyExtensionDatabaseRecovery(t *testing.T, f *channelFixture, api pb.ExtensionP1ServiceClient, host *mutationProbe, want int, mode string) {
	t.Helper()
	request := mutationRequest("database-fault-001", 0)
	mutationCounts(t, f, want, want, want, want)
	event := ""
	if want == 1 {
		event = verifyExtensionCrashData(t, f)
	}
	// Повтор проходит через прежний экземпляр Store: пул обязан восстановить соединения.
	response, err := api.ExecuteExtensionCommand(f.owner, request)
	if err != nil || response.GetReplayed() != (want == 1) || response.GetStateRevision() != 1 {
		t.Fatal(response, err)
	}
	mutationCounts(t, f, 1, 1, 1, 1)
	verifyExtensionCrashReceipt(t, f, response)
	if got := verifyExtensionCrashData(t, f); event != "" && got != event {
		t.Fatal("Повтор изменил ID события")
	}
	if host.calls.Load() != int32(2-want) {
		t.Fatal("Повторное выполнение гостя", host.calls.Load())
	}
	_, err = f.auth.Logout(f.owner, &pb.LogoutRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.ExecuteExtensionCommand(f.owner, request)
	requireCode(t, err, codes.Unauthenticated)
	t.Logf("mode=%s revision=1 data=1 receipts=1 outbox=1 replayed=%v calls=%d", mode, response.GetReplayed(), host.calls.Load())
}

func extensionDatabaseBarrier(t *testing.T, f *channelFixture) {
	t.Helper()
	gate, err := f.store.pool.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = gate.Exec(context.Background(), "SELECT pg_advisory_unlock(974422)"); gate.Release() })
	if _, err = gate.Exec(f.ctx, "SELECT pg_advisory_lock(974422)"); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.pool.Exec(f.ctx, `CREATE FUNCTION extension_p1_wait_commit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_advisory_xact_lock(974422); RETURN NEW; END$$;
 CREATE TRIGGER wait_commit BEFORE INSERT ON extension_p1_outbox FOR EACH ROW EXECUTE FUNCTION extension_p1_wait_commit()`)
	if err != nil {
		t.Fatal(err)
	}
}

func restartExtensionDatabase(t *testing.T, f *channelFixture) {
	t.Helper()
	id := os.Getenv("SPACE_P1_DATABASE_CONTAINER")
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id) {
		t.Fatal("Неверный ID выделенного контейнера CI")
	}
	u, err := url.Parse(f.url)
	if err != nil {
		t.Fatal(err)
	}
	image := extensionDocker(t, "inspect", "--format", "{{.Config.Image}}", id)
	port := extensionDocker(t, "port", id, "5432/tcp")
	if strings.TrimSpace(image) != "postgres:17.11" || !strings.Contains(port, ":"+u.Port()+"\n") {
		t.Fatal("Контейнер не соответствует тестовой БД")
	}
	extensionDocker(t, "kill", "--signal=KILL", id)
	if state := extensionDocker(t, "inspect", "--format", "{{.State.ExitCode}}", id); strings.TrimSpace(state) != "137" {
		t.Fatal("PostgreSQL не завершена SIGKILL", state)
	}
	extensionDocker(t, "start", id)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		f.store.pool.Reset()
		ctx, cancel := context.WithTimeout(f.ctx, time.Second)
		err := f.store.pool.Ping(ctx)
		cancel()
		if err == nil {
			t.Log("PostgreSQL восстановлена после SIGKILL, exit=137")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("PostgreSQL не восстановлена")
}

func extensionDocker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %s %v", args, data, err)
	}
	return string(data)
}
