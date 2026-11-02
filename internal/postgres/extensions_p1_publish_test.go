package postgres

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

type publishFixture struct {
	u          *upgradeFixture
	host       *extensions.ProcessHost
	target     ExtensionP1MutationBinding
	modules    map[string][]byte
	oldRequest *pb.ExecuteExtensionCommandRequest
}

func publishModule(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(os.Getenv("SPACE_P1_PUBLISH_MODULES"), name+".wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newPublishFixture(t *testing.T, kind string) *publishFixture {
	t.Helper()
	if os.Getenv("SPACE_P1_PUBLISH_MODULES") == "" {
		t.Skip("Нужен изолированный CI публикации")
	}
	host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), os.Getenv("SPACE_P1_WORKER"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	f := newChannelFixture(t)
	channel := f.create(t, "publish_test")
	bindings := mutationBindings(channel.Id, publishModule(t, "mutation"))
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, host, bindings)
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	api := extensionP1RPCClient(t, f, s)
	request := mutationRequest("publish-source-command", 0)
	if _, err = api.ExecuteExtensionCommand(f.owner, request); err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "disable", 2)
	if _, err = s.SystemTransition(f.ctx, "install", "finish_disable", 3); err != nil {
		t.Fatal(err)
	}
	target := mutationBindings(channel.Id, publishModule(t, "upgraded"))["demo"]
	sourceHash, targetHash := mutationPackageHash(bindings["demo"]), mutationPackageHash(target)
	module := publishModule(t, "migrate-"+kind)
	p := upgrade.Plan{Version: upgrade.Version, Space: f.identity.ServerID, Installation: "install", Generation: s.Generation(), Revision: 4, DataRevision: 1, Source: hex.EncodeToString(sourceHash[:]), Target: hex.EncodeToString(targetHash[:]), Grants: strings.Repeat("3", 64), Checkpoint: strings.Repeat("6", 64), SchemaFrom: 1, SchemaTo: 2, Steps: []upgrade.Step{{ID: "convert", From: 1, To: 2, Digest: upgradeHash(module)}}}
	digest, _ := p.Digest()
	u := &upgradeFixture{f: f, service: s, bindings: bindings, plan: p, digest: digest, verification: upgrade.Verification{Plan: digest, Target: p.Target, Grants: p.Grants, Trusted: true, Compatible: true, Approved: true}, checkpoint: upgrade.Checkpoint{Plan: digest, Digest: p.Checkpoint, Source: p.Source, Schema: 1, DataRevision: 1, Complete: true}}
	return &publishFixture{u, host, target, map[string][]byte{"convert": module}, request}
}

func (p *publishFixture) prepare(t *testing.T) {
	t.Helper()
	u := p.u
	if err := u.service.PrepareWASMUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "demo", u.plan, u.verification, u.checkpoint, p.target, p.modules); err != nil {
		t.Fatal(err)
	}
}
func (p *publishFixture) resume() (ExtensionP1UpgradeProgress, error) {
	u := p.u
	return u.service.ResumeWASMUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install", p.host, func(context.Context, upgrade.Plan) (upgrade.Verification, upgrade.Checkpoint, error) {
		return u.verification, u.checkpoint, nil
	})
}
func (p *publishFixture) publish() error {
	u := p.u
	return u.service.PublishUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install", u.digest, u.verification, u.checkpoint)
}
func (p *publishFixture) reopen(t *testing.T) {
	t.Helper()
	u := p.u
	store, _, err := Open(u.f.ctx, u.f.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	u.service, err = store.NewExtensionP1ManagedService(u.f.ctx, p.host, u.bindings)
	if err != nil {
		t.Fatal(err)
	}
}

func (p *publishFixture) state(t *testing.T, published bool) {
	t.Helper()
	u := p.u
	var hash string
	var value []byte
	var revision int64
	err := u.f.store.pool.QueryRow(u.f.ctx, `SELECT encode(i.package_hash,'hex'),d.value,r.revision FROM extension_p1_instances i JOIN extension_p1_data d USING(installation,instance) JOIN extension_p1_installations r USING(installation) WHERE i.instance='demo' AND d.key='note'`).Scan(&hash, &value, &revision)
	wantHash, wantValue, wantRevision, wantData := u.plan.Source, `{"value":1}`, int64(4), 1
	if published {
		wantHash, wantValue, wantRevision, wantData = u.plan.Target, `{"value":2}`, 5, 2
	}
	if err != nil || hash != wantHash || string(value) != wantValue || revision != wantRevision {
		t.Fatal(hash, string(value), revision, err)
	}
	mutationCounts(t, u.f, 1, wantData, 1, 1)
}

func TestExtensionP1PublishWASM(t *testing.T) {
	p := newPublishFixture(t, "ok")
	p.prepare(t)
	u := p.u
	result, err := p.resume()
	if err != nil || !result.Ready() {
		t.Fatal(result, err)
	}
	p.state(t, false)
	if err = p.publish(); err != nil {
		t.Fatal(err)
	}
	p.state(t, true)
	_, err = u.service.ReadUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install")
	requireCode(t, err, codes.Unavailable)
	p.reopen(t)
	result, err = u.service.ReadUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install")
	if err != nil || !result.Published {
		t.Fatal(result, err)
	}
	if err = p.publish(); err != nil {
		t.Fatal(err)
	} // Повтор не публикует второй раз.
	lifecycleRegistryState(t, u.f, "disabled", 5)
	managedChange(t, u.f, u.service, "enable", 5)
	api := extensionP1RPCClient(t, u.f, u.service)
	query, err := api.QueryExtension(u.f.owner, extensionP1Query())
	if err != nil || !strings.Contains(string(query.GetDocumentJson()), "Upgraded") {
		t.Fatal(query, err)
	}
	_, err = api.ExecuteExtensionCommand(u.f.owner, p.oldRequest)
	requireCode(t, err, codes.FailedPrecondition)
	if _, err = api.ExecuteExtensionCommand(u.f.owner, mutationRequest("publish-target-command", 1)); err != nil {
		t.Fatal(err)
	}
	mutationCounts(t, u.f, 2, 2, 2, 2)
	if err = CheckExtensionP1Backup(u.f.ctx, u.f.url, false); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionP1PublishWASMFailures(t *testing.T) {
	for _, kind := range []string{"trap", "loop", "invalid", "schema", "memory"} {
		t.Run(kind, func(t *testing.T) {
			p := newPublishFixture(t, kind)
			p.prepare(t)
			if _, err := p.resume(); err == nil {
				t.Fatal("Принят сбойный WASM")
			}
			stats := p.host.Stats()
			if stats.Active != 0 || stats.ReservedBytes != 0 {
				t.Fatal("Не освобождена квота", stats)
			}
			if err := p.publish(); err == nil {
				t.Fatal("Опубликован незавершённый результат")
			}
			p.state(t, false)
			p.reopen(t)
			progress, err := p.u.service.ReadUpgrade(p.u.f.ctx, lifecycleToken(p.u.f.owner), "install")
			if err != nil || progress.NextStep != 0 {
				t.Fatal(progress, err)
			}
		})
	}
}

func TestExtensionP1PublishConcurrent(t *testing.T) {
	p := newPublishFixture(t, "ok")
	p.prepare(t)
	if _, err := p.resume(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var success atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.publish() == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatal(success.Load())
	}
	p.state(t, true)
	var count int
	if err := p.u.f.store.pool.QueryRow(p.u.f.ctx, `SELECT count(*) FROM extension_p1_lifecycle_audit WHERE action='publish_upgrade'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestExtensionP1PublishRejectsUntrusted(t *testing.T) {
	for _, kind := range []string{"module_hash", "target_hash", "changed_data", "go_step", "revoked", "member"} {
		t.Run(kind, func(t *testing.T) {
			p := newPublishFixture(t, "ok")
			u := p.u
			if kind == "module_hash" {
				p.modules["convert"] = []byte{1}
			}
			if kind == "target_hash" {
				p.target.Registration.Module = []byte{1}
			}
			if kind == "module_hash" || kind == "target_hash" {
				if err := u.service.PrepareWASMUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "demo", u.plan, u.verification, u.checkpoint, p.target, p.modules); err == nil {
					t.Fatal("Принята подмена артефакта")
				}
				return
			}
			p.prepare(t)
			if kind == "go_step" {
				if _, err := u.advance(0, func(context.Context, upgrade.Step, []byte) ([]byte, error) {
					return []byte(`{"version":"space.migration.data.p1","schema":2,"values":{"note":{"value":2}}}`), nil
				}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := p.resume(); err != nil {
				t.Fatal(err)
			}
			if kind == "changed_data" {
				if _, err := u.f.store.pool.Exec(u.f.ctx, `UPDATE extension_p1_data SET value='{}'::bytea`); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "revoked" {
				u.verification.Trusted = false
			}
			if kind == "member" {
				member, _ := u.f.enroll(t, true)
				err := u.service.PublishUpgrade(u.f.ctx, lifecycleToken(member), "install", u.digest, u.verification, u.checkpoint)
				requireCode(t, err, codes.PermissionDenied)
				return
			}
			if err := p.publish(); err == nil {
				t.Fatal("Принята неподтверждённая публикация")
			}
		})
	}
}
