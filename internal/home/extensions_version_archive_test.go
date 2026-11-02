package home

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var versionArchiveStates = []string{"published", "pending", "ready"}

type versionArchiveFixture struct {
	Registry registryTransferFixture
	State    string
	Snapshot map[string]string
}

func versionArchiveSnapshot(t *testing.T, address string) map[string]string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, `SET TIME ZONE 'UTC'`); err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, name := range []string{"installations", "lifecycle_audit", "instances", "data", "receipts", "outbox", "versions", "upgrade_jobs", "upgrade_steps", "upgrade_artifacts"} {
		expression := "to_jsonb(r)"
		if name == "installations" {
			expression += "-'generation'"
		}
		query := "SELECT COALESCE(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT " + expression + " AS v FROM extension_p1_" + name + " r) q"
		var raw string
		if err = conn.QueryRow(ctx, query).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		result[name] = raw
	}
	return result
}

func readVersionArchive(t *testing.T, directory, state string) versionArchiveFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(directory, state+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m versionArchiveFixture
	if err = json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.State != state || m.Registry.Format != 1 || m.Registry.SourceSHA != os.Getenv("GITHUB_SHA") || len(m.Registry.DevicePrivate) != ed25519.PrivateKeySize {
		t.Fatal("Неверный эталон архива")
	}
	return m
}

func writeVersionArchive(t *testing.T, directory string, m versionArchiveFixture) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, m.State+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// Импорт не запускает гостя. Модули и все байты истории сравниваются с источником.
func checkVersionArchive(t *testing.T, destination string, m versionArchiveFixture, delivery, portable bool) registryTransferFixture {
	t.Helper()
	ctx := context.Background()
	address, stop, err := StartDatabase(os.Getenv("SPACE_HOME_RUNTIME"), filepath.Join(destination, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	}()
	if !reflect.DeepEqual(versionArchiveSnapshot(t, address), m.Snapshot) {
		t.Fatal("Изменены история, пакеты, схемы или рабочий журнал")
	}
	store, id, err := postgres.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if id.ServerID != m.Registry.ServerID {
		t.Fatal("Изменилась идентичность")
	}
	_, err = store.Authenticate(ctx, m.Registry.OldToken)
	if portable && status.Code(err) != codes.Unauthenticated {
		t.Fatal("ZIP перенёс исходную сессию", err)
	}
	if !portable && err != nil {
		t.Fatal("Физическая копия изменила действующую сессию", err)
	}
	member, token := backupExtensionLogin(t, store, id, m.Registry.Grant, ed25519.PrivateKey(m.Registry.DevicePrivate))
	host := new(backupExtensionHost)
	svc, err := store.NewExtensionP1ManagedService(ctx, host, backupExtensionBindings())
	if err != nil {
		t.Fatal(err)
	}
	if svc.Generation() == m.Registry.Generation {
		t.Fatal("Поколение не изменилось")
	}
	versions, err := svc.ListUpgradeVersions(ctx, token, "install", 0, 100)
	count := 1
	if m.State == "published" {
		count = 2
	}
	if err != nil || len(versions) != count {
		t.Fatal(versions, err)
	}
	progress, err := svc.ReadUpgrade(ctx, token, "install")
	if err != nil || progress.Published != (m.State == "published") || progress.Ready() != (m.State != "pending") || progress.Plan.SchemaFrom != 2 || progress.Plan.SchemaTo != 3 {
		t.Fatal(progress, err)
	}
	if _, err = svc.Change(ctx, token, m.Registry.Generation, "install", "enable", versions[len(versions)-1].Revision); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Принято старое поколение", err)
	}
	checkVersionArchiveReceipt(t, address, svc, member, m.Registry)
	if delivery {
		checkRegistryTransferCursor(t, store, m.Registry)
	}
	if host.calls.Load() != 0 {
		t.Fatal("Восстановление запустило гостя")
	}
	if !reflect.DeepEqual(versionArchiveSnapshot(t, address), m.Snapshot) {
		t.Fatal("Открытие изменило архивные данные")
	}
	if err = postgres.CheckExtensionP1Backup(ctx, address, false); err != nil {
		t.Fatal(err)
	}
	next := m.Registry
	next.Generation = svc.Generation()
	next.OldToken = token
	return next
}

func TestExtensionP1VersionArchiveExport(t *testing.T) {
	seed, directory := os.Getenv("SPACE_P1_VERSION_SEED"), os.Getenv("SPACE_P1_VERSION_EXPORT")
	if seed == "" || directory == "" {
		t.Skip("Нужен межплатформенный стенд истории")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, state := range versionArchiveStates {
		t.Run(state, func(t *testing.T) {
			m := readVersionArchive(t, seed, state)
			source := filepath.Join(seed, state+".zip")
			before := registryArchiveDigest(t, source, true)
			if before != m.Registry.ArchiveSHA {
				t.Fatal("Изменён исходный ZIP")
			}
			destination := filepath.Join(t.TempDir(), "source")
			if err := ImportPortable(os.Getenv("SPACE_HOME_RUNTIME"), source, destination, ""); err != nil {
				t.Fatal(err)
			}
			m.Registry = checkVersionArchive(t, destination, m, false, true)
			archive := filepath.Join(directory, state+".zip")
			if err := ExportPortable(os.Getenv("SPACE_HOME_RUNTIME"), filepath.Join(destination, "data"), archive); err != nil {
				t.Fatal(err)
			}
			m.Registry.SourceOS = runtime.GOOS
			m.Registry.SourceArch = runtime.GOARCH
			m.Registry.ArchiveSHA = registryArchiveDigest(t, archive, true)
			writeVersionArchive(t, directory, m)
			// Оба формата восстанавливаются на исходной ОС из той же истории.
			for _, portable := range []bool{false, true} {
				t.Run(map[bool]string{false: "physical", true: "zip"}[portable], func(t *testing.T) {
					output := filepath.Join(t.TempDir(), "restored")
					var err error
					if portable {
						err = ImportPortable(os.Getenv("SPACE_HOME_RUNTIME"), archive, output, "")
					} else {
						backup := filepath.Join(t.TempDir(), "backup")
						err = CreateBackup(os.Getenv("SPACE_HOME_RUNTIME"), filepath.Join(destination, "data"), backup)
						if err == nil {
							err = RestoreBackup(os.Getenv("SPACE_HOME_RUNTIME"), backup, output)
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					checkVersionArchive(t, output, m, true, portable)
				})
			}
			if registryArchiveDigest(t, source, true) != before {
				t.Fatal("Изменён исходный ZIP")
			}
			t.Logf("state=%s source=%s archive_sha256=%s", state, runtime.GOOS, m.Registry.ArchiveSHA)
		})
	}
}

func TestExtensionP1VersionArchiveImport(t *testing.T) {
	directory := os.Getenv("SPACE_P1_VERSION_IMPORT")
	if directory == "" {
		t.Skip("Нужны ZIP других ОС")
	}
	for _, source := range []string{"linux", "windows", "darwin"} {
		if source == runtime.GOOS {
			continue
		}
		for _, state := range versionArchiveStates {
			t.Run(source+"_to_"+runtime.GOOS+"/"+state, func(t *testing.T) {
				folder := filepath.Join(directory, "version-transfer-"+source)
				m := readVersionArchive(t, folder, state)
				if m.Registry.SourceOS != source {
					t.Fatal("Неверная исходная ОС")
				}
				archive := filepath.Join(folder, state+".zip")
				if registryArchiveDigest(t, archive, true) != m.Registry.ArchiveSHA {
					t.Fatal("Изменён ZIP")
				}
				output := filepath.Join(t.TempDir(), "restored")
				if err := ImportPortable(os.Getenv("SPACE_HOME_RUNTIME"), archive, output, ""); err != nil {
					t.Fatal(err)
				}
				checkVersionArchive(t, output, m, true, true)
				if registryArchiveDigest(t, archive, true) != m.Registry.ArchiveSHA {
					t.Fatal("Импорт изменил ZIP")
				}
				t.Logf("source=%s target=%s state=%s archive_sha256=%s history_artifacts_preserved=true guest_calls=0", source, runtime.GOOS, state, m.Registry.ArchiveSHA)
			})
		}
	}
}

// Обновление оставляет установку выключенной: сверка квитанции доступна,
// но новый контекст команд не запрашивается через выключенный lifecycle.
func checkVersionArchiveReceipt(t *testing.T, address string, svc *postgres.ExtensionP1ManagedService, member context.Context, m registryTransferFixture) {
	t.Helper()
	request := new(pb.ExecuteExtensionCommandRequest)
	if err := proto.Unmarshal(m.Request, request); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var history string
	if err = conn.QueryRow(context.Background(), `SELECT history_id FROM extension_p1_meta WHERE singleton`).Scan(&history); err != nil || history == request.HistoryId {
		t.Fatal("История не сменилась", err)
	}
	if _, err = svc.ExecuteExtensionCommand(member, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Старая команда принята", err)
	}
	result, err := svc.ReconcileExtensionCommand(member, &pb.ReconcileExtensionCommandRequest{Command: request})
	if status.Code(err) != codes.AlreadyExists || result != nil {
		t.Fatal("Квитанция прежнего пакета принята новым пакетом", err)
	}
	var raw []byte
	if err = conn.QueryRow(context.Background(), `SELECT response FROM extension_p1_receipts WHERE key=$1`, request.IdempotencyKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	first, stored := new(pb.ExecuteExtensionCommandResponse), new(pb.ExecuteExtensionCommandResponse)
	if err = proto.Unmarshal(m.Receipt, first); err != nil {
		t.Fatal(err)
	}
	if err = proto.Unmarshal(raw, stored); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(first, stored) {
		t.Fatal("Квитанция изменилась")
	}
}
