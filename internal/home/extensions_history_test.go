package home

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/extensionp1"
	"github.com/space-protocol-project/space-server/internal/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExtensionP1OldBackupRestore(t *testing.T) {
	runtime := os.Getenv("SPACE_HOME_RUNTIME")
	if runtime == "" {
		t.Skip("Нужен настоящий домашний runtime")
	}
	for _, portable := range []bool{false, true} {
		name := "physical"
		if portable {
			name = "portable"
		}
		t.Run(name, func(t *testing.T) { extensionOldBackupRestore(t, runtime, portable) })
	}
}

type historyBackupFixture struct {
	t                                   *testing.T
	ctx                                 context.Context
	runtime, root, data, address, grant string
	devicePrivate                       ed25519.PrivateKey
	stop                                func() error
	store                               *postgres.Store
	identity                            postgres.Identity
	member                              context.Context
	host                                *backupExtensionHost
	service                             *extensionp1.Service
}

func extensionOldBackupRestore(t *testing.T, runtime string, portable bool) {
	f := newHistoryBackupFixture(t, runtime)
	request := &pb.ExecuteExtensionCommandRequest{InstanceId: "demo", Command: "acknowledge", PayloadJson: []byte(`{}`), Effect: pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_MUTATION, IdempotencyKey: "old-backup-command-001", HistoryId: f.history()}
	f.close()
	backup := f.save(portable)
	f.start(f.data)
	f.execute(request)
	f.close()
	f.restore(backup, portable)
	current := f.rejectOldRequest(request)
	next := &pb.ExecuteExtensionCommandRequest{InstanceId: request.InstanceId, Command: request.Command, PayloadJson: []byte(`{}`), Effect: request.Effect, IdempotencyKey: "new-history-command-001", HistoryId: current}
	f.execute(next)
	if f.host.calls.Load() != 1 {
		t.Fatal("Старая команда вызвала WASM")
	}
	t.Logf("portable=%v old_history_rejected=true old_receipt_absent=true new_command_passed=true", portable)
}

func newHistoryBackupFixture(t *testing.T, runtime string) *historyBackupFixture {
	t.Helper()
	f := &historyBackupFixture{t: t, ctx: context.Background(), runtime: runtime, root: t.TempDir()}
	f.data = filepath.Join(f.root, "data")
	t.Cleanup(f.close)
	f.start(f.data)
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	f.devicePrivate = devicePrivate
	auth := postgres.NewAuth(f.store, f.identity.ServerID, "http://127.0.0.1:8080")
	grant := backupExtensionProof(t, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: public, DevicePublicKey: device, Administrative: true}, private)
	f.grant = grant.GrantId
	f.bind()
	if _, err := Certificate(f.data, net.ParseIP("203.0.113.10")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "config.json"), []byte(`{"ip":"203.0.113.10","port":8443}`), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *historyBackupFixture) start(data string) {
	f.t.Helper()
	address, stop, err := StartDatabase(f.runtime, data)
	if err != nil {
		f.t.Fatal(err)
	}
	f.address, f.stop = address, stop
	store, id, err := postgres.Open(f.ctx, address)
	if err != nil {
		f.t.Fatal(err)
	}
	f.store = store
	if f.identity.ServerID != "" && f.identity.ServerID != id.ServerID {
		f.t.Fatal("Изменилась идентичность сервера")
	}
	f.identity = id
	if f.grant != "" {
		f.bind()
	}
}

func (f *historyBackupFixture) bind() {
	f.t.Helper()
	f.member, _ = backupExtensionLogin(f.t, f.store, f.identity, f.grant, f.devicePrivate)
	f.host = new(backupExtensionHost)
	service, err := f.store.NewExtensionP1MutationService(f.ctx, f.host, backupExtensionBindings())
	if err != nil {
		f.t.Fatal(err)
	}
	f.service = service
}

func (f *historyBackupFixture) close() {
	f.t.Helper()
	if f.store != nil {
		f.store.Close()
		f.store = nil
	}
	if f.stop != nil {
		if err := f.stop(); err != nil {
			f.t.Error(err)
		}
		f.stop = nil
	}
}

func (f *historyBackupFixture) save(portable bool) string {
	f.t.Helper()
	path := filepath.Join(f.root, "before-command")
	var err error
	if portable {
		path += ".zip"
		err = ExportPortable(f.runtime, f.data, path)
	} else {
		err = CreateBackup(f.runtime, f.data, path)
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *historyBackupFixture) restore(backup string, portable bool) {
	f.t.Helper()
	destination := filepath.Join(f.root, "restored")
	var err error
	if portable {
		err = ImportPortable(f.runtime, backup, destination, "")
	} else {
		err = RestoreBackup(f.runtime, backup, destination)
	}
	if err != nil {
		f.t.Fatal(err)
	}
	f.start(filepath.Join(destination, "data"))
}

func (f *historyBackupFixture) history() string {
	f.t.Helper()
	result, err := f.service.GetExtensionMutationContext(f.member, &pb.GetExtensionMutationContextRequest{InstanceId: "demo"})
	if err != nil {
		f.t.Fatal(err)
	}
	return result.HistoryId
}

func (f *historyBackupFixture) execute(request *pb.ExecuteExtensionCommandRequest) {
	f.t.Helper()
	if _, err := f.service.ExecuteExtensionCommand(f.member, request); err != nil {
		f.t.Fatal(err)
	}
	checkBackupExtensionData(f.t, f.address)
}

func (f *historyBackupFixture) rejectOldRequest(request *pb.ExecuteExtensionCommandRequest) string {
	f.t.Helper()
	current := f.history()
	if current == request.HistoryId {
		f.t.Fatal("История не сменилась")
	}
	for range 2 {
		if _, err := f.service.ExecuteExtensionCommand(f.member, request); status.Code(err) != codes.FailedPrecondition {
			f.t.Fatal(err)
		}
	}
	result, err := f.service.ReconcileExtensionCommand(f.member, &pb.ReconcileExtensionCommandRequest{Command: request})
	if err != nil || result.Outcome != pb.ExtensionCommandOutcome_EXTENSION_COMMAND_OUTCOME_UNKNOWN || result.Receipt != nil || f.host.calls.Load() != 0 {
		f.t.Fatal(result, err, f.host.calls.Load())
	}
	checkEmptyExtensionHistory(f.t, f.address)
	return current
}

func checkEmptyExtensionHistory(t *testing.T, address string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var count int
	err = conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM extension_p1_data)+(SELECT count(*) FROM extension_p1_receipts)+(SELECT count(*) FROM extension_p1_outbox)+(SELECT sum(revision) FROM extension_p1_instances)`).Scan(&count)
	if err != nil || count != 0 {
		t.Fatal("Старая команда изменила восстановленную копию", count, err)
	}
}
