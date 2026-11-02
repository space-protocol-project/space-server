//go:build linux

package home

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/postgres"
	"google.golang.org/protobuf/proto"
)

func TestExtensionP1VersionArchiveSeed(t *testing.T) {
	directory := os.Getenv("SPACE_P1_VERSION_CREATE")
	if directory == "" {
		t.Skip("Нужен Linux-стенд подготовки WASM-истории")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, state := range versionArchiveStates {
		t.Run(state, func(t *testing.T) {
			f := newHistoryBackupFixture(t, os.Getenv("SPACE_HOME_RUNTIME"))
			svc, err := f.store.NewExtensionP1ManagedService(f.ctx, f.host, backupExtensionBindings())
			if err != nil {
				t.Fatal(err)
			}
			token := authn.Token(f.member)
			registryArchiveChange(t, svc, token, "install", "enable", 1)
			h, err := svc.GetExtensionMutationContext(f.member, &pb.GetExtensionMutationContextRequest{InstanceId: "demo"})
			if err != nil {
				t.Fatal(err)
			}
			request := &pb.ExecuteExtensionCommandRequest{InstanceId: "demo", Command: "acknowledge", PayloadJson: []byte(`{}`), Effect: pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_MUTATION, IdempotencyKey: "version-archive-command", HistoryId: h.HistoryId}
			receipt, err := svc.ExecuteExtensionCommand(f.member, request)
			if err != nil {
				t.Fatal(err)
			}
			confirmed, leased := prepareRegistryTransferCursor(t, f.store)
			registryArchiveChange(t, svc, token, "install", "disable", 2)
			if _, err = svc.SystemTransition(f.ctx, "install", "finish_disable", 3); err != nil {
				t.Fatal(err)
			}
			runVersionArchiveWorker(t, f.address, token, state)
			m := versionArchiveFixture{State: state, Snapshot: versionArchiveSnapshot(t, f.address), Registry: registryTransferFixture{Format: 1, SourceOS: "linux", SourceArch: "amd64", SourceSHA: os.Getenv("GITHUB_SHA"), ServerID: f.identity.ServerID, Grant: f.grant, OldToken: token, DevicePrivate: f.devicePrivate, ConfirmedToken: confirmed, Leased: *leased}}
			conn, err := pgx.Connect(f.ctx, f.address)
			if err != nil {
				t.Fatal(err)
			}
			err = conn.QueryRow(f.ctx, `SELECT generation FROM extension_p1_installations WHERE installation='install'`).Scan(&m.Registry.Generation)
			conn.Close(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			m.Registry.Request, err = proto.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			m.Registry.Receipt, err = proto.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			f.close()
			archive := filepath.Join(directory, state+".zip")
			if err = ExportPortable(f.runtime, f.data, archive); err != nil {
				t.Fatal(err)
			}
			m.Registry.ArchiveSHA = registryArchiveDigest(t, archive, true)
			writeVersionArchive(t, directory, m)
			t.Logf("state=%s wasm_migrations=true archive_sha256=%s", state, m.Registry.ArchiveSHA)
		})
	}
}

func runVersionArchiveWorker(t *testing.T, address, token, state string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// PostgreSQL принадлежит обычному runner; только изолированный WASM-хост требует root.
	cmd := exec.Command("sudo", "--preserve-env=SPACE_P1_CGROUP,SPACE_P1_WORKER,SPACE_P1_PUBLISH_MODULES,SPACE_P1_VERSION_DATABASE,SPACE_P1_VERSION_TOKEN,SPACE_P1_VERSION_STATE", executable, "-test.run=^TestExtensionP1VersionArchiveWorker$", "-test.timeout=2m")
	cmd.Env = append(os.Environ(), "SPACE_P1_VERSION_DATABASE="+address, "SPACE_P1_VERSION_TOKEN="+token, "SPACE_P1_VERSION_STATE="+state)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("WASM-подготовка: %v\n%s", err, output)
	}
}

func versionArchiveHash(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }

func TestExtensionP1VersionArchiveWorker(t *testing.T) {
	address := os.Getenv("SPACE_P1_VERSION_DATABASE")
	if address == "" {
		return
	}
	ctx := context.Background()
	host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), os.Getenv("SPACE_P1_WORKER"))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	store, identity, err := postgres.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, state := os.Getenv("SPACE_P1_VERSION_TOKEN"), os.Getenv("SPACE_P1_VERSION_STATE")
	if state == "resume" {
		resumeVersionArchive(t, store, host, token)
		return
	}
	for index := 0; index < 2; index++ {
		svc, err := store.NewExtensionP1ManagedService(ctx, host, backupExtensionBindings())
		if err != nil {
			t.Fatal(err)
		}
		target := backupExtensionBindings()["demo"]
		target.Registration.Module = versionArchiveModule(t, []string{"upgraded", "upgraded3"}[index])
		raw, err := json.Marshal([]any{target.Registration.Module, target.Registration.Manifest, target.Events})
		if err != nil {
			t.Fatal(err)
		}
		module := versionArchiveModule(t, []string{"migrate-ok", "migrate-next"}[index])
		p := upgrade.Plan{Version: upgrade.Version, Space: identity.ServerID, Installation: "install", Generation: svc.Generation(), Grants: strings.Repeat("3", 64), Checkpoint: strings.Repeat("6", 64), SchemaFrom: uint32(index + 1), SchemaTo: uint32(index + 2), Target: versionArchiveHash(raw), Steps: []upgrade.Step{{ID: "convert", From: uint32(index + 1), To: uint32(index + 2), Digest: versionArchiveHash(module)}}}
		conn, err := pgx.Connect(ctx, address)
		if err != nil {
			t.Fatal(err)
		}
		err = conn.QueryRow(ctx, `SELECT encode(i.package_hash,'hex'),i.revision,r.revision FROM extension_p1_instances i JOIN extension_p1_installations r USING(installation) WHERE i.instance='demo'`).Scan(&p.Source, &p.DataRevision, &p.Revision)
		conn.Close(ctx)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := p.Digest()
		if err != nil {
			t.Fatal(err)
		}
		v := upgrade.Verification{Plan: digest, Target: p.Target, Grants: p.Grants, Trusted: true, Compatible: true, Approved: true}
		c := upgrade.Checkpoint{Plan: digest, Digest: p.Checkpoint, Source: p.Source, Schema: p.SchemaFrom, DataRevision: p.DataRevision, Complete: true}
		if err = svc.PrepareWASMUpgrade(ctx, token, "demo", p, v, c, target, map[string][]byte{"convert": module}); err != nil {
			t.Fatal(err)
		}
		if index == 1 && state == "pending" {
			return
		}
		if _, err = svc.ResumeWASMUpgrade(ctx, token, "install", host, func(context.Context, upgrade.Plan) (upgrade.Verification, upgrade.Checkpoint, error) {
			return v, c, nil
		}); err != nil {
			t.Fatal(err)
		}
		if index == 1 && state == "ready" {
			return
		}
		if err = svc.PublishUpgrade(ctx, token, "install", digest, v, c); err != nil {
			t.Fatal(err)
		}
	}
}

func versionArchiveModule(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(os.Getenv("SPACE_P1_PUBLISH_MODULES"), name+".wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestExtensionP1VersionArchiveResume(t *testing.T) {
	directory := os.Getenv("SPACE_P1_VERSION_IMPORT")
	if directory == "" || os.Getenv("SPACE_P1_WORKER") == "" {
		t.Skip("Нужен Linux-стенд возобновления импортированного WASM")
	}
	for _, source := range []string{"windows", "darwin"} {
		for _, state := range versionArchiveStates {
			t.Run(source+"_to_linux/"+state, func(t *testing.T) {
				folder := filepath.Join(directory, "version-transfer-"+source)
				m := readVersionArchive(t, folder, state)
				archive := filepath.Join(folder, state+".zip")
				if registryArchiveDigest(t, archive, true) != m.Registry.ArchiveSHA {
					t.Fatal("Изменён ZIP")
				}
				output := filepath.Join(t.TempDir(), "restored")
				runtime := os.Getenv("SPACE_HOME_RUNTIME")
				if err := ImportPortable(runtime, archive, output, ""); err != nil {
					t.Fatal(err)
				}
				checkVersionArchive(t, output, m, false)
				withRegistryArchiveDatabase(t, runtime, filepath.Join(output, "data"), func(address string) {
					store, id, err := postgres.Open(context.Background(), address)
					if err != nil {
						t.Fatal(err)
					}
					defer store.Close()
					_, token := backupExtensionLogin(t, store, id, m.Registry.Grant, m.Registry.DevicePrivate)
					before := versionArchiveSnapshot(t, address)
					runVersionArchiveWorker(t, address, token, "resume")
					after := versionArchiveSnapshot(t, address)
					if before["receipts"] != after["receipts"] || before["outbox"] != after["outbox"] {
						t.Fatal("Возобновление изменило квитанции или события")
					}
				})
				if registryArchiveDigest(t, archive, true) != m.Registry.ArchiveSHA {
					t.Fatal("Изменён исходный ZIP")
				}
				t.Logf("source=%s target=linux state=%s wasm_resume=true published_versions=2", source, state)
			})
		}
	}
}

func resumeVersionArchive(t *testing.T, store *postgres.Store, host *extensions.ProcessHost, token string) {
	t.Helper()
	ctx := context.Background()
	svc, err := store.NewExtensionP1ManagedService(ctx, host, backupExtensionBindings())
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.ListUpgradeVersions(ctx, token, "install", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := svc.ReadUpgrade(ctx, token, "install")
	if err != nil {
		t.Fatal(err)
	}
	p := progress.Plan
	v := upgrade.Verification{Plan: progress.Digest, Target: p.Target, Grants: p.Grants, Trusted: true, Compatible: true, Approved: true}
	c := upgrade.Checkpoint{Plan: progress.Digest, Digest: p.Checkpoint, Source: p.Source, Schema: p.SchemaFrom, DataRevision: p.DataRevision, Complete: true}
	if !progress.Published {
		if _, err = svc.ResumeWASMUpgrade(ctx, token, "install", host, func(context.Context, upgrade.Plan) (upgrade.Verification, upgrade.Checkpoint, error) {
			return v, c, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err = svc.PublishUpgrade(ctx, token, "install", progress.Digest, v, c); err != nil {
		t.Fatal(err)
	}
	svc, err = store.NewExtensionP1ManagedService(ctx, host, backupExtensionBindings())
	if err != nil {
		t.Fatal(err)
	}
	after, err := svc.ListUpgradeVersions(ctx, token, "install", 0, 100)
	if err != nil || len(after) != 2 || after[0] != before[0] || after[1].SchemaTo != 3 {
		t.Fatal(after, err)
	}
	if err = svc.PublishUpgrade(ctx, token, "install", progress.Digest, v, c); err != nil {
		t.Fatal(err)
	}
}
