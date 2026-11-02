package postgres

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
	"google.golang.org/grpc/codes"
)

type groupFixture struct {
	f        *channelFixture
	service  *ExtensionP1ManagedService
	host     *extensions.ProcessHost
	bindings map[string]ExtensionP1MutationBinding
	members  []ExtensionP1GroupMember
	evidence map[string]ExtensionP1GroupEvidence
	digest   string
}

func newGroupFixture(t *testing.T) *groupFixture {
	t.Helper()
	if os.Getenv("SPACE_P1_PUBLISH_MODULES") == "" {
		t.Skip("Нужен изолированный CI миграций")
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
	a, b := f.create(t, "group_a"), f.create(t, "group_b")
	bindings := mutationBindings(a.Id, publishModule(t, "mutation"))
	bindings["other"] = mutationBindings(b.Id, publishModule(t, "mutation"))["demo"]
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, host, bindings)
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	api := extensionP1RPCClient(t, f, s)
	if _, err = api.ExecuteExtensionCommand(f.owner, mutationRequest("group-source-command", 0)); err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "disable", 2)
	if _, err = s.SystemTransition(f.ctx, "install", "finish_disable", 3); err != nil {
		t.Fatal(err)
	}
	g := &groupFixture{f: f, service: s, host: host, bindings: bindings}
	g.plan(t, 1)
	return g
}

func (g *groupFixture) plan(t *testing.T, schema uint32) {
	t.Helper()
	sourceName, targetName := "mutation", "upgraded"
	modules := []string{"ok", "empty"}
	if schema == 2 {
		sourceName, targetName = "upgraded", "upgraded3"
		modules = []string{"next", "empty-next"}
	}
	var revision int64
	if err := g.f.store.pool.QueryRow(g.f.ctx, `SELECT revision FROM extension_p1_installations WHERE installation='install'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	g.members = nil
	for i, id := range []string{"demo", "other"} {
		b := g.bindings[id]
		source := mutationBindings(b.Channel, publishModule(t, sourceName))["demo"]
		target := mutationBindings(b.Channel, publishModule(t, targetName))["demo"]
		sourceHash, targetHash := mutationPackageHash(source), mutationPackageHash(target)
		module := publishModule(t, "migrate-"+modules[i])
		var dataRevision uint64
		if err := g.f.store.pool.QueryRow(g.f.ctx, `SELECT revision FROM extension_p1_instances WHERE instance=$1`, id).Scan(&dataRevision); err != nil {
			t.Fatal(err)
		}
		p := upgrade.Plan{Version: upgrade.Version, Space: g.f.identity.ServerID, Installation: "install", Generation: g.service.Generation(), Revision: revision, DataRevision: dataRevision, Source: hex.EncodeToString(sourceHash[:]), Target: hex.EncodeToString(targetHash[:]), Grants: strings.Repeat("3", 64), Checkpoint: strings.Repeat("6", 64), SchemaFrom: schema, SchemaTo: schema + 1, Steps: []upgrade.Step{{ID: "convert", From: schema, To: schema + 1, Digest: upgradeHash(module)}}}
		g.members = append(g.members, ExtensionP1GroupMember{Instance: id, Plan: p, Target: target, Modules: map[string][]byte{"convert": module}})
	}
	g.attest(t)
}

func (g *groupFixture) attest(t *testing.T) {
	t.Helper()
	var err error
	g.digest, err = ExtensionP1GroupDigest(g.members)
	if err != nil {
		t.Fatal(err)
	}
	g.evidence = map[string]ExtensionP1GroupEvidence{}
	for _, m := range g.members {
		p := m.Plan
		digest, _ := p.Digest()
		g.evidence[m.Instance] = ExtensionP1GroupEvidence{Digest: g.digest, Verification: upgrade.Verification{Plan: digest, Target: p.Target, Grants: p.Grants, Trusted: true, Compatible: true, Approved: true}, Checkpoint: upgrade.Checkpoint{Plan: digest, Digest: p.Checkpoint, Source: p.Source, Schema: p.SchemaFrom, DataRevision: p.DataRevision, Complete: true}}
	}
}

func (g *groupFixture) prepare() error {
	return g.service.PrepareGroupUpgrade(g.f.ctx, lifecycleToken(g.f.owner), g.members, g.evidence)
}
func (g *groupFixture) advance(id string) error {
	_, err := g.service.AdvanceGroupUpgrade(g.f.ctx, lifecycleToken(g.f.owner), "install", g.digest, id, 0, g.host, g.evidence[id])
	return err
}
func (g *groupFixture) publish() error {
	return g.service.PublishGroupUpgrade(g.f.ctx, lifecycleToken(g.f.owner), "install", g.digest, g.evidence)
}
func (g *groupFixture) read(t *testing.T) ExtensionP1GroupProgress {
	t.Helper()
	p, err := g.service.ReadGroupUpgrade(g.f.ctx, lifecycleToken(g.f.owner), "install")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (g *groupFixture) reopen(t *testing.T) {
	t.Helper()
	store, _, err := Open(g.f.ctx, g.f.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	g.service, err = store.NewExtensionP1ManagedService(g.f.ctx, g.host, g.bindings)
	if err != nil {
		t.Fatal(err)
	}
}
func (g *groupFixture) state(t *testing.T, published bool) {
	t.Helper()
	for _, m := range g.members {
		var hash string
		var revision uint64
		if err := g.f.store.pool.QueryRow(g.f.ctx, `SELECT encode(package_hash,'hex'),revision FROM extension_p1_instances WHERE instance=$1`, m.Instance).Scan(&hash, &revision); err != nil {
			t.Fatal(err)
		}
		want := m.Plan.Source
		if published {
			want = m.Plan.Target
		}
		if hash != want || revision != m.Plan.DataRevision {
			t.Fatal(m.Instance, hash, revision)
		}
		schema := m.Plan.SchemaFrom
		if published {
			schema = m.Plan.SchemaTo
		}
		tx, err := g.f.store.pool.Begin(g.f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := readMigrationData(g.f.ctx, tx, "install", m.Instance, schema)
		_ = tx.Rollback(g.f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var values map[string]any
		var data struct {
			Values map[string]any `json:"values"`
		}
		if err = json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		values = data.Values
		if m.Instance == "demo" {
			if _, ok := values["note"]; !ok {
				t.Fatal(string(raw))
			}
			if _, ok := values["other"]; ok {
				t.Fatal("Данные экземпляров смешаны")
			}
		} else {
			if _, ok := values["note"]; ok {
				t.Fatal("Данные экземпляров смешаны")
			}
			if schema > 1 && values["other"] != true {
				t.Fatal(string(raw))
			}
		}
	}
	if err := CheckExtensionP1Backup(g.f.ctx, g.f.url, false); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionP1GroupAtomicFailure(t *testing.T) {
	g := newGroupFixture(t)
	if err := g.prepare(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"demo", "other"} {
		if err := g.advance(id); err != nil {
			t.Fatal(err)
		}
	}
	_, err := g.f.store.pool.Exec(g.f.ctx, `CREATE FUNCTION reject_group_publish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.instance='other' THEN RAISE EXCEPTION 'CI failure on second instance'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_group_publish BEFORE UPDATE ON extension_p1_instances FOR EACH ROW EXECUTE FUNCTION reject_group_publish()`)
	if err != nil {
		t.Fatal(err)
	}
	if err = g.publish(); err == nil {
		t.Fatal("Подтверждена частичная публикация")
	}
	g.state(t, false)
	if _, err = g.f.store.pool.Exec(g.f.ctx, `DROP TRIGGER reject_group_publish ON extension_p1_instances; DROP FUNCTION reject_group_publish()`); err != nil {
		t.Fatal(err)
	}
	g.reopen(t)
	var wg sync.WaitGroup
	var successes atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.publish() == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal(successes.Load())
	}
	g.reopen(t)
	g.state(t, true)
}

func TestExtensionP1GroupSequential(t *testing.T) {
	g := newGroupFixture(t)
	for schema := uint32(1); schema <= 2; schema++ {
		if schema == 2 {
			g.plan(t, schema)
		}
		if err := g.prepare(); err != nil {
			t.Fatal(err)
		}
		if err := g.advance("demo"); err != nil {
			t.Fatal(err)
		}
		g.state(t, false)
		if err := g.publish(); err == nil {
			t.Fatal("Опубликована часть установки")
		}
		g.reopen(t)
		p := g.read(t)
		if p.NextSteps["demo"] != 1 || p.NextSteps["other"] != 0 || p.Ready {
			t.Fatal(p)
		}
		if err := g.advance("demo"); err == nil {
			t.Fatal("Повтор шага принят")
		}
		if err := g.advance("other"); err != nil {
			t.Fatal(err)
		}
		if err := g.publish(); err != nil {
			t.Fatal(err)
		}
		_, err := g.service.ReadGroupUpgrade(g.f.ctx, lifecycleToken(g.f.owner), "install")
		requireCode(t, err, codes.Unavailable)
		g.reopen(t)
		if err = g.publish(); err != nil {
			t.Fatal(err)
		}
		g.state(t, true)
		var versions, audits int
		if err = g.f.store.pool.QueryRow(g.f.ctx, `SELECT (SELECT count(*) FROM extension_p1_group_versions),(SELECT count(*) FROM extension_p1_lifecycle_audit WHERE action='publish_upgrade')`).Scan(&versions, &audits); err != nil || versions != int(schema) || audits != versions {
			t.Fatal(versions, audits, err)
		}
	}
	managedChange(t, g.f, g.service, "enable", 6)
	api := extensionP1RPCClient(t, g.f, g.service)
	for _, id := range []string{"demo", "other"} {
		request := extensionP1Query()
		request.InstanceId = id
		if _, err := api.QueryExtension(g.f.owner, request); err != nil {
			t.Fatal(id, err)
		}
	}
}

func TestExtensionP1GroupRejects(t *testing.T) {
	g := newGroupFixture(t)
	if err := g.service.PrepareGroupUpgrade(g.f.ctx, lifecycleToken(g.f.owner), g.members[:1], g.evidence); err == nil {
		t.Fatal("Принято подмножество")
	}
	_, err := g.service.ReadGroupUpgrade(g.f.ctx, "invalid", "install")
	requireCode(t, err, codes.Unauthenticated)
	member, _ := g.f.enroll(t, true)
	err = g.service.PrepareGroupUpgrade(g.f.ctx, lifecycleToken(member), g.members, g.evidence)
	requireCode(t, err, codes.PermissionDenied)
	evidence := g.evidence["other"]
	evidence.Digest = strings.Repeat("0", 64)
	g.evidence["other"] = evidence
	if err = g.prepare(); err == nil {
		t.Fatal("Принято чужое свидетельство состава")
	}
	g.attest(t)
	if err = g.prepare(); err != nil {
		t.Fatal(err)
	}
	if err = g.prepare(); err == nil {
		t.Fatal("Перезаписан незавершённый план")
	}
	if _, err = g.service.Change(g.f.ctx, lifecycleToken(g.f.owner), g.service.Generation(), "install", "enable", 4); err == nil {
		t.Fatal("Включена мигрирующая установка")
	}
	if err = g.advance("absent"); err == nil {
		t.Fatal("Принят неизвестный экземпляр")
	}
}

func TestExtensionP1GroupFailure(t *testing.T) {
	g := newGroupFixture(t)
	// Второй модуль ожидает другой документ: изолированный WASM аварийно завершается.
	module := publishModule(t, "migrate-trap")
	g.members[1].Modules["convert"] = module
	g.members[1].Plan.Steps[0].Digest = upgradeHash(module)
	g.attest(t)
	if err := g.prepare(); err != nil {
		t.Fatal(err)
	}
	if err := g.advance("demo"); err != nil {
		t.Fatal(err)
	}
	if err := g.advance("other"); err == nil {
		t.Fatal("Принят аварийный результат")
	}
	if err := g.publish(); err == nil {
		t.Fatal("Опубликована частичная миграция")
	}
	g.state(t, false)
	g.reopen(t)
	p := g.read(t)
	if p.NextSteps["demo"] != 1 || p.NextSteps["other"] != 0 {
		t.Fatal(p)
	}
	if stats := g.host.Stats(); stats.Active != 0 || stats.ReservedBytes != 0 {
		t.Fatal(stats)
	}
}
