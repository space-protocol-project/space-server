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

func extensionOldBackupRestore(t *testing.T, runtime string, portable bool) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	address, stop, err := StartDatabase(runtime, data)
	if err != nil {
		t.Fatal(err)
	}
	active := true
	defer func() {
		if active {
			if err := stop(); err != nil {
				t.Error(err)
			}
		}
	}()
	s, id, err := postgres.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	auth := postgres.NewAuth(s, id.ServerID, "http://127.0.0.1:8080")
	grant := backupExtensionProof(t, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: public, DevicePublicKey: device, Administrative: true}, private)
	member, _ := backupExtensionLogin(t, s, id, grant.GrantId, devicePrivate)
	host := new(backupExtensionHost)
	svc, err := s.NewExtensionP1MutationService(ctx, host, backupExtensionBindings())
	if err != nil {
		t.Fatal(err)
	}
	history, err := svc.GetExtensionMutationContext(member, &pb.GetExtensionMutationContextRequest{InstanceId: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	r := &pb.ExecuteExtensionCommandRequest{InstanceId: "demo", Command: "acknowledge", PayloadJson: []byte(`{}`), Effect: pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_MUTATION, IdempotencyKey: "old-backup-command-001", HistoryId: history.HistoryId}
	if _, err = Certificate(data, net.ParseIP("203.0.113.10")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"ip":"203.0.113.10","port":8443}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	active = false
	backup := filepath.Join(root, "before-command")
	if portable {
		backup += ".zip"
		err = ExportPortable(runtime, data, backup)
	} else {
		err = CreateBackup(runtime, data, backup)
	}
	if err != nil {
		t.Fatal(err)
	}
	// Команда выполняется уже после сохранения архива.
	address, stop, err = StartDatabase(runtime, data)
	if err != nil {
		t.Fatal(err)
	}
	active = true
	s, id, err = postgres.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	member, _ = backupExtensionLogin(t, s, id, grant.GrantId, devicePrivate)
	svc, err = s.NewExtensionP1MutationService(ctx, host, backupExtensionBindings())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ExecuteExtensionCommand(member, r); err != nil {
		t.Fatal(err)
	}
	checkBackupExtensionData(t, address)
	s.Close()
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	active = false
	destination := filepath.Join(root, "restored")
	if portable {
		err = ImportPortable(runtime, backup, destination, "")
	} else {
		err = RestoreBackup(runtime, backup, destination)
	}
	if err != nil {
		t.Fatal(err)
	}
	address, stop, err = StartDatabase(runtime, filepath.Join(destination, "data"))
	if err != nil {
		t.Fatal(err)
	}
	active = true
	s, id, err = postgres.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	member, _ = backupExtensionLogin(t, s, id, grant.GrantId, devicePrivate)
	host = new(backupExtensionHost)
	svc, err = s.NewExtensionP1MutationService(ctx, host, backupExtensionBindings())
	if err != nil {
		t.Fatal(err)
	}
	current, err := svc.GetExtensionMutationContext(member, &pb.GetExtensionMutationContextRequest{InstanceId: "demo"})
	if err != nil || current.HistoryId == r.HistoryId {
		t.Fatal(current, err)
	}
	for range 2 {
		if _, err = svc.ExecuteExtensionCommand(member, r); status.Code(err) != codes.FailedPrecondition {
			t.Fatal(err)
		}
	}
	result, err := svc.ReconcileExtensionCommand(member, &pb.ReconcileExtensionCommandRequest{Command: r})
	if err != nil || result.Outcome != pb.ExtensionCommandOutcome_EXTENSION_COMMAND_OUTCOME_UNKNOWN || result.Receipt != nil || host.calls.Load() != 0 {
		t.Fatal(result, err, host.calls.Load())
	}
	checkEmptyExtensionHistory(t, address)
	// Это отдельное явно созданное действие, а не перепривязка старого запроса.
	next := &pb.ExecuteExtensionCommandRequest{InstanceId: r.InstanceId, Command: r.Command, PayloadJson: []byte(`{}`), Effect: r.Effect, IdempotencyKey: "new-history-command-001", HistoryId: current.HistoryId}
	if _, err = svc.ExecuteExtensionCommand(member, next); err != nil {
		t.Fatal(err)
	}
	checkBackupExtensionData(t, address)
	if host.calls.Load() != 1 {
		t.Fatal("Старая команда вызвала WASM")
	}
	t.Logf("portable=%v old_history_rejected=true old_receipt_absent=true new_command_passed=true", portable)
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
