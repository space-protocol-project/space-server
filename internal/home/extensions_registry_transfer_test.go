package home

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Только случайные учётные данные одноразового пространства CI.
// Файл передаётся вместе с тестовым архивом и не публикуется в отчёте.
type registryTransferFixture struct {
	Format                                      int
	SourceOS, SourceArch, SourceSHA, ArchiveSHA string
	ServerID, Grant, OldToken                   string
	DevicePrivate                               []byte
	Generation, Registry, Journal               string
	Request, Receipt                            []byte
	ConfirmedToken                              string
	Leased                                      postgres.ExtensionP1Delivery
	Retired                                     bool
}

func TestExtensionP1RegistryTransferExport(t *testing.T) {
	directory := os.Getenv("SPACE_P1_TRANSFER_EXPORT")
	if directory == "" {
		t.Skip("Нужен источник межплатформенного CI")
	}
	if os.Getenv("SPACE_HOME_RUNTIME") == "" || os.Getenv("GITHUB_SHA") == "" {
		t.Fatal("Не задан runtime или SHA")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"full", "retired"} {
		t.Run(name, func(t *testing.T) { exportRegistryTransfer(t, directory, name) })
	}
}

func exportRegistryTransfer(t *testing.T, directory, name string) {
	f := newHistoryBackupFixture(t, os.Getenv("SPACE_HOME_RUNTIME"))
	p, svc := newRegistryArchiveProbe(t, f.store, authn.Token(f.member), f.host)
	history, err := svc.GetExtensionMutationContext(f.member, &pb.GetExtensionMutationContextRequest{InstanceId: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	request := &pb.ExecuteExtensionCommandRequest{InstanceId: "demo", Command: "acknowledge", PayloadJson: []byte(`{}`), Effect: pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_MUTATION, IdempotencyKey: "transfer-command-001", HistoryId: history.HistoryId}
	receipt, err := svc.ExecuteExtensionCommand(f.member, request)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, leased := prepareRegistryTransferCursor(t, f.store)
	p.capture(t, f.address)
	m := registryTransferFixture{Format: 1, SourceOS: runtime.GOOS, SourceArch: runtime.GOARCH, SourceSHA: os.Getenv("GITHUB_SHA"), ServerID: f.identity.ServerID, Grant: f.grant, OldToken: authn.Token(f.member), DevicePrivate: f.devicePrivate, Generation: p.generation, Registry: p.registry, Journal: p.journal, ConfirmedToken: confirmed, Leased: *leased, Retired: name == "retired"}
	m.Request, err = proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	m.Receipt, err = proto.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if f.host.calls.Load() != 1 {
		t.Fatal("Исходная команда исполнена не один раз")
	}
	if m.Retired {
		f.store.Close()
		f.store = nil
		retireBackupExtensionReceipt(t, f.address)
	}
	f.close()
	archive := filepath.Join(directory, name+".zip")
	if err = ExportPortable(f.runtime, f.data, archive); err != nil {
		t.Fatal(err)
	}
	m.ArchiveSHA = registryArchiveDigest(t, archive, true)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, name+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("source=%s/%s variant=%s archive_sha256=%s", m.SourceOS, m.SourceArch, name, m.ArchiveSHA)
}

func prepareRegistryTransferCursor(t *testing.T, store *postgres.Store) (string, *postgres.ExtensionP1Delivery) {
	t.Helper()
	ctx := context.Background()
	outbox, err := store.NewExtensionP1Outbox(ctx, "install", "demo", "projection")
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := outbox.Claim(ctx)
	if err != nil || confirmed == nil {
		t.Fatal(confirmed, err)
	}
	if err = outbox.Ack(ctx, confirmed.Token); err != nil {
		t.Fatal(err)
	}
	leased, err := outbox.Claim(ctx)
	if err != nil || leased == nil || leased.Ordinal != 1 {
		t.Fatal(leased, err)
	}
	return confirmed.Token, leased
}

func TestExtensionP1RegistryTransferImport(t *testing.T) {
	directory := os.Getenv("SPACE_P1_TRANSFER_IMPORT")
	if directory == "" {
		t.Skip("Нужны архивы других ОС из CI")
	}
	if os.Getenv("SPACE_HOME_RUNTIME") == "" || os.Getenv("GITHUB_SHA") == "" {
		t.Fatal("Не задан runtime или SHA")
	}
	for _, source := range []string{"linux", "windows", "darwin"} {
		if source == runtime.GOOS {
			continue
		}
		for _, name := range []string{"full", "retired"} {
			t.Run(source+"_to_"+runtime.GOOS+"/"+name, func(t *testing.T) { importRegistryTransfer(t, directory, source, name) })
		}
	}
}

func importRegistryTransfer(t *testing.T, directory, source, name string) {
	directory = filepath.Join(directory, "transfer-"+source)
	raw, err := os.ReadFile(filepath.Join(directory, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m registryTransferFixture
	if err = json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Format != 1 || m.SourceOS != source || m.SourceOS == runtime.GOOS || m.SourceSHA != os.Getenv("GITHUB_SHA") || m.Retired != (name == "retired") || len(m.DevicePrivate) != ed25519.PrivateKeySize {
		t.Fatal("Неверный источник тестового архива")
	}
	archive := filepath.Join(directory, name+".zip")
	if registryArchiveDigest(t, archive, true) != m.ArchiveSHA {
		t.Fatal("Архив изменён при передаче")
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err = ImportPortable(os.Getenv("SPACE_HOME_RUNTIME"), archive, destination, ""); err != nil {
		t.Fatal(err)
	}
	address, stop, err := StartDatabase(os.Getenv("SPACE_HOME_RUNTIME"), filepath.Join(destination, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	}()
	store, id, err := postgres.Open(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if id.ServerID != m.ServerID {
		t.Fatal("Изменилась идентичность пространства")
	}
	if _, err = store.Authenticate(context.Background(), m.OldToken); status.Code(err) != codes.Unauthenticated {
		t.Fatal("Перенесена исходная сессия", err)
	}
	member, token := backupExtensionLogin(t, store, id, m.Grant, ed25519.PrivateKey(m.DevicePrivate))
	host := new(backupExtensionHost)
	p := &registryArchiveProbe{bindings: registryArchiveBindings(), generation: m.Generation, registry: m.Registry, journal: m.Journal}
	svc := p.restore(t, store, address, member, token, host)
	checkRegistryTransferReceipt(t, svc, member, m)
	checkRegistryTransferCursor(t, store, m)
	checkBackupExtensionData(t, address)
	if host.calls.Load() != 0 {
		t.Fatal("Перенос повторно исполнил гостевой код")
	}
	if registryArchiveDigest(t, archive, true) != m.ArchiveSHA {
		t.Fatal("Импорт изменил исходный ZIP")
	}
	t.Logf("source=%s/%s target=%s/%s variant=%s archive_sha256=%s registry_audit_preserved=true guest_calls=0", m.SourceOS, m.SourceArch, runtime.GOOS, runtime.GOARCH, name, m.ArchiveSHA)
}

func checkRegistryTransferReceipt(t *testing.T, svc *postgres.ExtensionP1ManagedService, member context.Context, m registryTransferFixture) {
	t.Helper()
	request := new(pb.ExecuteExtensionCommandRequest)
	if err := proto.Unmarshal(m.Request, request); err != nil {
		t.Fatal(err)
	}
	current, err := svc.GetExtensionMutationContext(member, &pb.GetExtensionMutationContextRequest{InstanceId: "demo"})
	if err != nil || current.HistoryId == request.HistoryId {
		t.Fatal("История не сменилась", err)
	}
	if _, err = svc.ExecuteExtensionCommand(member, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Старая команда принята", err)
	}
	result, err := svc.ReconcileExtensionCommand(member, &pb.ReconcileExtensionCommandRequest{Command: request})
	if m.Retired {
		if status.Code(err) != codes.FailedPrecondition || result != nil {
			t.Fatal("Удалённое тело квитанции восстановлено", err)
		}
		return
	}
	if err != nil || result.GetOutcome() != pb.ExtensionCommandOutcome_EXTENSION_COMMAND_OUTCOME_CONFIRMED || !result.GetReceipt().GetReplayed() {
		t.Fatal("Нет исходной квитанции", err)
	}
	first := new(pb.ExecuteExtensionCommandResponse)
	if err = proto.Unmarshal(m.Receipt, first); err != nil {
		t.Fatal(err)
	}
	result.Receipt.Replayed = false
	if !proto.Equal(first, result.Receipt) {
		t.Fatal("Квитанция изменилась")
	}
}

func checkRegistryTransferCursor(t *testing.T, store *postgres.Store, m registryTransferFixture) {
	t.Helper()
	ctx := context.Background()
	o, err := store.NewExtensionP1Outbox(ctx, "install", "demo", "projection")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{m.ConfirmedToken, m.Leased.Token} {
		if err = o.Ack(ctx, token); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("Старый токен доставки принят", err)
		}
	}
	next, err := o.Claim(ctx)
	if err != nil || next == nil || next.ID != m.Leased.ID || next.Ordinal != 1 || next.Token == m.Leased.Token || next.Attempt != m.Leased.Attempt+1 {
		t.Fatal("Курсор или попытка доставки изменились", err)
	}
	if err = o.Ack(ctx, next.Token); err != nil {
		t.Fatal(err)
	}
	pending, err := o.Claim(ctx)
	if err != nil || pending == nil || pending.Ordinal != 2 {
		t.Fatal("Потеряно ожидающее событие", err)
	}
}
