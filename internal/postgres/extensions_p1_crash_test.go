//go:build linux

package postgres

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

// Отдельный процесс содержит настоящий Store, executor и gRPC. Только гостевой
// план детерминирован: этот тест проверяет восстановление записи, не WASM worker.
func TestExtensionP1CrashProcess(t *testing.T) {
	database := os.Getenv("SPACE_P1_CRASH_DATABASE")
	if database == "" {
		return
	}
	ctx := context.Background()
	store, _, err := Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	host := &mutationProbe{run: func(context.Context) ([]byte, error) {
		file, err := os.OpenFile(os.Getenv("SPACE_P1_CRASH_CALLS"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		if _, err = file.WriteString("invoke\n"); err != nil {
			return nil, err
		}
		if err = file.Sync(); err != nil {
			return nil, err
		}
		return mutationOutput(0), nil
	}}
	svc, err := store.NewExtensionP1MutationService(ctx, host, mutationBindings("mut_crash", []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var withheld atomic.Bool
	barrier := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		response, err := handler(ctx, req)
		if err == nil && os.Getenv("SPACE_P1_CRASH_HOLD") == "yes" && withheld.CompareAndSwap(false, true) {
			if err = os.WriteFile(os.Getenv("SPACE_P1_CRASH_COMMITTED"), []byte("committed"), 0600); err != nil {
				return nil, err
			}
			<-ctx.Done() // Commit завершён, но ни одного байта ответа RPC ещё нет.
			return nil, ctx.Err()
		}
		return response, err
	}
	server := grpc.NewServer(grpc.ChainUnaryInterceptor(authn.Interceptor(store), barrier))
	pb.RegisterExtensionP1ServiceServer(server, svc)
	if err = os.WriteFile(os.Getenv("SPACE_P1_CRASH_ADDRESS"), []byte(listener.Addr().String()), 0600); err != nil {
		t.Fatal(err)
	}
	if err = server.Serve(listener); err != nil {
		t.Fatal(err)
	}
}

type extensionCrashProcess struct {
	command *exec.Cmd
	address string
	stopped bool
}

func startExtensionCrashProcess(t *testing.T, f *channelFixture, directory, calls, committed string, hold bool) *extensionCrashProcess {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	address := filepath.Join(directory, "address")
	log, err := os.Create(filepath.Join(directory, "process.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	command := exec.Command(executable, "-test.run=^TestExtensionP1CrashProcess$", "-test.timeout=30s")
	mode := "no"
	if hold {
		mode = "yes"
	}
	command.Env = append(os.Environ(), "SPACE_P1_CRASH_DATABASE="+f.url, "SPACE_P1_CRASH_CALLS="+calls, "SPACE_P1_CRASH_COMMITTED="+committed, "SPACE_P1_CRASH_ADDRESS="+address, "SPACE_P1_CRASH_HOLD="+mode)
	command.Stdout, command.Stderr = log, log
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	p := &extensionCrashProcess{command: command}
	t.Cleanup(func() { p.kill(t) })
	p.address = string(waitExtensionCrashFile(t, address))
	return p
}

func (p *extensionCrashProcess) kill(t *testing.T) {
	t.Helper()
	if p.stopped {
		return
	}
	p.stopped = true
	if err := p.command.Process.Kill(); err != nil {
		t.Error(err)
	}
	err := p.command.Wait()
	state, ok := p.command.ProcessState.Sys().(syscall.WaitStatus)
	if err == nil || !ok || !state.Signaled() || state.Signal() != syscall.SIGKILL {
		t.Error("Процесс не завершён принудительно", err)
	}
}

func waitExtensionCrashFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return data
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Не достигнут барьер дочернего процесса", path)
	return nil
}

func extensionCrashClient(t *testing.T, address string) (*grpc.ClientConn, pb.ExtensionP1ServiceClient) {
	t.Helper()
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, pb.NewExtensionP1ServiceClient(conn)
}

func TestExtensionP1MutationCrashRecovery(t *testing.T) {
	for _, mode := range []string{"before_commit", "after_commit", "lost_response"} {
		t.Run(mode, func(t *testing.T) { testExtensionCrashRecovery(t, mode) })
	}
}

func testExtensionCrashRecovery(t *testing.T, mode string) {
	t.Helper()
	f := newChannelFixture(t)
	f.create(t, "mut_crash")
	directory := t.TempDir()
	calls, committed := filepath.Join(directory, "calls"), filepath.Join(directory, "committed")
	p := startExtensionCrashProcess(t, f, t.TempDir(), calls, committed, mode != "before_commit")
	conn, api := extensionCrashClient(t, p.address)
	release := func() {}
	if mode == "before_commit" {
		release = extensionCommitBarrier(t, f)
	}
	request := mutationRequest("crash-recovery-001", 0)
	done := make(chan error, 1)
	go func() { _, err := api.ExecuteExtensionCommand(f.owner, request); done <- err }()
	var eventBefore string
	if mode == "before_commit" {
		waitExtensionSQLLock(t, f, "INSERT INTO extension_p1_outbox")
	} else {
		waitExtensionCrashFile(t, committed)
		eventBefore = verifyExtensionCrashData(t, f)
	}
	if mode == "lost_response" {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		p.kill(t)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Клиент получил потерянный ответ")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Оборванный RPC не завершился")
	}
	release()
	if mode == "before_commit" {
		// DDL ждёт завершения оборванной транзакции PostgreSQL, затем снимает барьер.
		if _, err := f.store.pool.Exec(f.ctx, "DROP TRIGGER wait_commit ON extension_p1_outbox"); err != nil {
			t.Fatal(err)
		}
		mutationCounts(t, f, 0, 0, 0, 0)
	} else {
		mutationCounts(t, f, 1, 1, 1, 1)
	}
	if mode != "lost_response" {
		p = startExtensionCrashProcess(t, f, t.TempDir(), calls, committed, false)
	}
	_, api = extensionCrashClient(t, p.address)
	response, err := api.ExecuteExtensionCommand(f.owner, request)
	if err != nil || response.GetStateRevision() != 1 || response.GetReplayed() != (mode != "before_commit") {
		t.Fatal(response, err)
	}
	mutationCounts(t, f, 1, 1, 1, 1)
	verifyExtensionCrashReceipt(t, f, response)
	if event := verifyExtensionCrashData(t, f); eventBefore != "" && event != eventBefore {
		t.Fatal("Повтор заменил сохранённое событие")
	}
	wantCalls := "invoke\n"
	if mode == "before_commit" {
		wantCalls += "invoke\n"
	}
	if got := string(waitExtensionCrashFile(t, calls)); got != wantCalls {
		t.Fatalf("Вызовы гостя: %q, ожидались %q", got, wantCalls)
	}
	_, err = f.auth.Logout(f.owner, &pb.LogoutRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.ExecuteExtensionCommand(f.owner, request)
	requireCode(t, err, codes.Unauthenticated)
	t.Logf("mode=%s revision=1 data=1 receipts=1 outbox=1 replayed=%v", mode, response.GetReplayed())
}

func verifyExtensionCrashData(t *testing.T, f *channelFixture) string {
	t.Helper()
	var value, event, kind, payload string
	err := f.store.pool.QueryRow(f.ctx, `SELECT convert_from(d.value,'UTF8'),o.event_id,o.type,convert_from(o.payload,'UTF8')
 FROM extension_p1_data d JOIN extension_p1_outbox o USING(installation,instance)
 WHERE d.instance='demo' AND d.key='note' AND o.revision=1 AND o.ordinal=0`).Scan(&value, &event, &kind, &payload)
	if err != nil || value != `{"value":1}` || event == "" || kind != "note_changed" || payload != "{}" {
		t.Fatal("Данные или событие повреждены", value, event, kind, payload, err)
	}
	return event
}

func verifyExtensionCrashReceipt(t *testing.T, f *channelFixture, response *pb.ExecuteExtensionCommandResponse) {
	t.Helper()
	var raw []byte
	if err := f.store.pool.QueryRow(f.ctx, "SELECT response FROM extension_p1_receipts WHERE instance='demo'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	saved := new(pb.ExecuteExtensionCommandResponse)
	if err := proto.Unmarshal(raw, saved); err != nil {
		t.Fatal(err)
	}
	actual := proto.Clone(response).(*pb.ExecuteExtensionCommandResponse)
	actual.Replayed = false
	if !proto.Equal(saved, actual) {
		t.Fatal("Ответ не совпал с сохранённой квитанцией")
	}
}
