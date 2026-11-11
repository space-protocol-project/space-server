package postgres

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExtensionP1HistoryBudgetAccounting(t *testing.T) {
	u := newUpgradeFixture(t)
	tx, err := u.f.store.pool.Begin(u.f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	// Отдельные временные таблицы проверяют суммирование двух форматов и установок.
	for _, table := range []string{"versions", "group_versions"} {
		if _, err = tx.Exec(u.f.ctx, `CREATE TEMP TABLE extension_p1_`+table+` (installation text, record bytea) ON COMMIT DROP`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(u.f.ctx, `INSERT INTO extension_p1_versions VALUES ('a',decode('010203','hex')); INSERT INTO extension_p1_group_versions VALUES ('b',decode('0405','hex')),('a',decode('06070809','hex'))`); err != nil {
		t.Fatal(err)
	}
	actual, err := readHistoryUsage(u.f.ctx, tx, "a")
	if err != nil || actual != (historyUsage{Records: 2, Bytes: 7, TotalRecords: 3, TotalBytes: 9}) {
		t.Fatal(actual, err)
	}
	if _, err = tx.Exec(u.f.ctx, `INSERT INTO extension_p1_group_versions SELECT 'b',decode('00','hex') FROM generate_series(1,125)`); err != nil {
		t.Fatal(err)
	}
	if err = admitHistory(u.f.ctx, tx, "a", 1); status.Code(err) != codes.ResourceExhausted {
		t.Fatal(err)
	}
}

func TestExtensionP1HistoryBudgetBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		u    historyUsage
		size int64
		ok   bool
	}{
		{"exact_bytes", historyUsage{Bytes: historyInstallationBytes - 1, TotalBytes: historyTotalBytes - 1}, 1, true},
		{"installation_bytes", historyUsage{Bytes: historyInstallationBytes}, 1, false},
		{"total_bytes", historyUsage{TotalBytes: historyTotalBytes}, 1, false},
		{"last_record", historyUsage{Records: 31, TotalRecords: 127}, 1, true},
		{"installation_records", historyUsage{Records: 32}, 1, false},
		{"total_records", historyUsage{TotalRecords: 128}, 1, false},
		{"oversize", historyUsage{}, historyInstallationBytes + 1, false},
		{"negative", historyUsage{}, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.u.admit(tc.size)
			if tc.ok && err != nil || !tc.ok && status.Code(err) != codes.ResourceExhausted {
				t.Fatal(err)
			}
		})
	}
}

func historySnapshot(t *testing.T, f *channelFixture) string {
	t.Helper()
	var result strings.Builder
	for _, table := range []string{"meta", "installations", "lifecycle_audit", "instances", "data", "receipts", "outbox", "versions", "group_versions", "group_jobs", "upgrade_jobs", "upgrade_steps", "upgrade_artifacts"} {
		var raw string
		if err := f.store.pool.QueryRow(f.ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM extension_p1_`+table+` t`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		result.WriteString(raw)
	}
	return result.String()
}

func TestExtensionP1VersionsHistoryQuota(t *testing.T) {
	p := firstVersion(t)
	nextVersion(t, p, false)
	p.prepare(t)
	if _, err := p.resume(); err != nil {
		t.Fatal(err)
	}
	if err := p.publish(); err != nil {
		t.Fatal(err)
	}
	p.reopen(t)
	for n := int64(3); n <= historyInstallationRecords+1; n++ {
		nextVersion(t, p, true)
		u := p.u
		if n%2 == 0 {
			p.target.Registration.Module = publishModule(t, "upgraded3")
			hash := mutationPackageHash(p.target)
			u.plan.Target = hex.EncodeToString(hash[:])
			u.digest, _ = u.plan.Digest()
			u.verification.Plan, u.verification.Target = u.digest, u.plan.Target
			u.checkpoint.Plan = u.digest
		}
		p.prepare(t)
		before := historySnapshot(t, u.f)
		err := p.publish()
		if n > historyInstallationRecords {
			if status.Code(err) != codes.ResourceExhausted || before != historySnapshot(t, u.f) {
				t.Fatal(err, "Отказ изменил состояние")
			}
			versionList(t, p, int(historyInstallationRecords))
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		p.reopen(t)
		if err := p.publish(); err != nil {
			t.Fatal("Идемпотентный повтор", err)
		}
	}
}

func TestExtensionP1GroupHistoryQuota(t *testing.T) {
	g := newGroupFixture(t)
	if err := g.prepare(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"demo", "other"} {
		if err := g.advance(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.publish(); err != nil {
		t.Fatal(err)
	}
	g.reopen(t)
	for n := int64(2); n <= historyInstallationRecords+1; n++ {
		planHistoryGroup(t, g, n)
		if err := g.prepare(); err != nil {
			t.Fatal(err)
		}
		before := historySnapshot(t, g.f)
		err := g.publish()
		if n > historyInstallationRecords {
			if status.Code(err) != codes.ResourceExhausted || before != historySnapshot(t, g.f) {
				t.Fatal(err, "Отказ изменил группу")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		g.reopen(t)
		if err := g.publish(); err != nil {
			t.Fatal("Идемпотентный повтор группы", err)
		}
	}
}

func planHistoryGroup(t *testing.T, g *groupFixture, n int64) {
	t.Helper()
	name := "upgraded-code"
	if n%2 == 1 {
		name = "upgraded"
	}
	for i := range g.members {
		m := &g.members[i]
		p := &m.Plan
		p.Source, p.SchemaFrom = p.Target, p.SchemaTo
		p.Generation = g.service.Generation()
		if err := g.f.store.pool.QueryRow(g.f.ctx, `SELECT r.revision,i.revision FROM extension_p1_installations r JOIN extension_p1_instances i USING(installation) WHERE i.instance=$1`, m.Instance).Scan(&p.Revision, &p.DataRevision); err != nil {
			t.Fatal(err)
		}
		m.Target.Registration.Module = publishModule(t, name)
		hash := mutationPackageHash(m.Target)
		p.Target = hex.EncodeToString(hash[:])
		p.Steps = []upgrade.Step{}
		m.Modules = map[string][]byte{}
	}
	g.attest(t)
}
