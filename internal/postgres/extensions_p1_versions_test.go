package postgres

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
	"google.golang.org/grpc/codes"
)

func firstVersion(t *testing.T) *publishFixture {
	t.Helper()
	p := newPublishFixture(t, "ok")
	p.prepare(t)
	if _, err := p.resume(); err != nil {
		t.Fatal(err)
	}
	if err := p.publish(); err != nil {
		t.Fatal(err)
	}
	p.reopen(t)
	return p
}

func nextVersion(t *testing.T, p *publishFixture, codeOnly bool) {
	t.Helper()
	u := p.u
	var source string
	var revision int64
	var dataRevision uint64
	if err := u.f.store.pool.QueryRow(u.f.ctx, `SELECT encode(i.package_hash,'hex'),i.revision,r.revision FROM extension_p1_instances i JOIN extension_p1_installations r USING(installation) WHERE i.instance='demo'`).Scan(&source, &dataRevision, &revision); err != nil {
		t.Fatal(err)
	}
	name := "upgraded3"
	from, to := uint32(2), uint32(3)
	steps := []upgrade.Step{{ID: "convert_next", From: 2, To: 3, Digest: upgradeHash(publishModule(t, "migrate-next"))}}
	modules := map[string][]byte{"convert_next": publishModule(t, "migrate-next")}
	if codeOnly {
		name = "upgraded-code"
		from = 3
		steps = []upgrade.Step{}
		modules = map[string][]byte{}
	}
	p.target = mutationBindings("publish_test", publishModule(t, name))["demo"]
	hash := mutationPackageHash(p.target)
	u.plan = upgrade.Plan{Version: upgrade.Version, Space: u.f.identity.ServerID, Installation: "install", Generation: u.service.Generation(), Revision: revision, DataRevision: dataRevision, Source: source, Target: hex.EncodeToString(hash[:]), Grants: strings.Repeat("3", 64), Checkpoint: strings.Repeat("7", 64), SchemaFrom: from, SchemaTo: to, Steps: steps}
	u.digest, _ = u.plan.Digest()
	p.modules = modules
	u.verification = upgrade.Verification{Plan: u.digest, Target: u.plan.Target, Grants: u.plan.Grants, Trusted: true, Compatible: true, Approved: true}
	u.checkpoint = upgrade.Checkpoint{Plan: u.digest, Digest: u.plan.Checkpoint, Source: source, Schema: from, DataRevision: dataRevision, Complete: true}
}

func versionList(t *testing.T, p *publishFixture, want int) []ExtensionP1Version {
	t.Helper()
	u := p.u
	list, err := u.service.ListUpgradeVersions(u.f.ctx, lifecycleToken(u.f.owner), "install", 0, 100)
	if err != nil || len(list) != want {
		t.Fatal(list, err)
	}
	for i, v := range list {
		if v.Sequence != int64(i+1) {
			t.Fatal(list)
		}
	}
	return list
}

func TestExtensionP1VersionsSequential(t *testing.T) {
	p := firstVersion(t)
	u := p.u
	first := versionList(t, p, 1)[0]
	var archived string
	if err := u.f.store.pool.QueryRow(u.f.ctx, `SELECT record_hash FROM extension_p1_versions WHERE sequence=1`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	nextVersion(t, p, false)
	p.prepare(t)
	versionList(t, p, 1)
	p.reopen(t) // Активный пакет берётся из истории, пока следующий план ещё не выполнен.
	if _, err := p.resume(); err != nil {
		t.Fatal(err)
	}
	if err := p.publish(); err != nil {
		t.Fatal(err)
	}
	p.reopen(t)
	list := versionList(t, p, 2)
	if list[0] != first || list[1].Source != first.Target || list[1].SchemaFrom != 2 || list[1].SchemaTo != 3 {
		t.Fatal(list)
	}
	page, err := u.service.ListUpgradeVersions(u.f.ctx, lifecycleToken(u.f.owner), "install", 1, 1)
	if err != nil || len(page) != 1 || page[0] != list[1] {
		t.Fatal(page, err)
	}
	nextVersion(t, p, true)
	p.prepare(t)
	if result, err := p.resume(); err != nil || !result.Ready() {
		t.Fatal(result, err)
	}
	if err = p.publish(); err != nil {
		t.Fatal(err)
	}
	p.reopen(t)
	list = versionList(t, p, 3)
	if list[2].SchemaFrom != 3 || list[2].SchemaTo != 3 || list[2].Source != list[1].Target {
		t.Fatal(list)
	}
	var current string
	if err = u.f.store.pool.QueryRow(u.f.ctx, `SELECT record_hash FROM extension_p1_versions WHERE sequence=1`).Scan(&current); err != nil || current != archived {
		t.Fatal(current, err)
	}
	managedChange(t, u.f, u.service, "enable", list[2].Revision)
	api := extensionP1RPCClient(t, u.f, u.service)
	query, err := api.QueryExtension(u.f.owner, extensionP1Query())
	if err != nil || !strings.Contains(string(query.GetDocumentJson()), "Code update") {
		t.Fatal(query, err)
	}
	if _, err = api.ExecuteExtensionCommand(u.f.owner, mutationRequest("versions-latest-command", 1)); err != nil {
		t.Fatal(err)
	}
	mutationCounts(t, u.f, 2, 3, 2, 2)
	if err = CheckExtensionP1Backup(u.f.ctx, u.f.url, false); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionP1VersionsPermissionsAndSource(t *testing.T) {
	p := firstVersion(t)
	u := p.u
	member, _ := u.f.enroll(t, true)
	_, err := u.service.ListUpgradeVersions(u.f.ctx, lifecycleToken(member), "install", 0, 10)
	requireCode(t, err, codes.PermissionDenied)
	_, err = u.service.ListUpgradeVersions(u.f.ctx, "invalid", "install", 0, 10)
	requireCode(t, err, codes.Unauthenticated)
	if _, err = u.service.ListUpgradeVersions(u.f.ctx, lifecycleToken(u.f.owner), "install", 0, 101); err == nil {
		t.Fatal("Нет ограничения страницы")
	}
	nextVersion(t, p, false)
	// Согласованные свидетельства не могут подменить фактическую исходную схему.
	invalid := u.plan
	invalid.SchemaFrom = 1
	invalid.SchemaTo = 2
	invalid.Steps = []upgrade.Step{{ID: "convert_next", From: 1, To: 2, Digest: u.plan.Steps[0].Digest}}
	digest, _ := invalid.Digest()
	v, c := u.verification, u.checkpoint
	v.Plan = digest
	c.Plan = digest
	c.Schema = 1
	if err = u.service.PrepareWASMUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "demo", invalid, v, c, p.target, p.modules); err == nil {
		t.Fatal("Принята старая схема")
	}
	p.prepare(t)
	if err = u.service.PrepareWASMUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "demo", u.plan, u.verification, u.checkpoint, p.target, p.modules); err == nil {
		t.Fatal("Перезаписан незавершённый план")
	}
	versionList(t, p, 1)
}

func TestExtensionP1VersionsBackfill(t *testing.T) {
	p := firstVersion(t)
	before := versionList(t, p, 1)
	// Точная форма метаданных предыдущего прототипа: опубликованный job без истории.
	if _, err := p.u.f.store.pool.Exec(p.u.f.ctx, `DROP TABLE extension_p1_versions; ALTER TABLE extension_p1_meta DROP COLUMN versions_version`); err != nil {
		t.Fatal(err)
	}
	p.reopen(t)
	after := versionList(t, p, 1)
	if after[0] != before[0] {
		t.Fatal(before, after)
	}
	nextVersion(t, p, false)
	p.prepare(t)
	if _, err := p.resume(); err != nil {
		t.Fatal(err)
	}
	if err := p.publish(); err != nil {
		t.Fatal(err)
	}
	p.reopen(t)
	versionList(t, p, 2)
}

func TestExtensionP1VersionsCorruption(t *testing.T) {
	for _, sql := range []string{
		`UPDATE extension_p1_versions SET record_hash=repeat('0',64) WHERE sequence=1`,
		`UPDATE extension_p1_versions SET source=repeat('0',64) WHERE sequence=2`,
		`DELETE FROM extension_p1_versions WHERE sequence=1`,
		`DELETE FROM extension_p1_versions`,
		`DROP TABLE extension_p1_versions`,
	} {
		t.Run(sql, func(t *testing.T) {
			p := firstVersion(t)
			nextVersion(t, p, false)
			p.prepare(t)
			if _, err := p.resume(); err != nil {
				t.Fatal(err)
			}
			if err := p.publish(); err != nil {
				t.Fatal(err)
			}
			if _, err := p.u.f.store.pool.Exec(p.u.f.ctx, sql); err != nil {
				t.Fatal(err)
			}
			if err := CheckExtensionP1Backup(p.u.f.ctx, p.u.f.url, false); err == nil {
				t.Fatal("Принята повреждённая история")
			}
			store, _, err := Open(p.u.f.ctx, p.u.f.url)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, err = store.NewExtensionP1ManagedService(p.u.f.ctx, p.host, p.u.bindings); err == nil {
				t.Fatal("Открыта повреждённая история")
			}
		})
	}
}
