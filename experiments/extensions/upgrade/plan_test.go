package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/space-protocol-project/space-server/experiments/extensions/lifecycle"
)

func fixture() (Plan, Binding, Verification, Checkpoint, Migration) {
	p := Plan{Version: Version, Space: "space-demo", Installation: "install", Generation: strings.Repeat("a", 32), Revision: 7, DataRevision: 19, Source: strings.Repeat("1", 64), Target: strings.Repeat("2", 64), Grants: strings.Repeat("3", 64), Checkpoint: strings.Repeat("6", 64), SchemaFrom: 1, SchemaTo: 3, Steps: []Step{{"add_index", 1, 2, strings.Repeat("4", 64)}, {"backfill", 2, 3, strings.Repeat("5", 64)}}}
	digest, _ := p.Digest()
	b := Binding{p.Space, p.Installation, p.Generation, p.Source, p.Revision, p.DataRevision, p.SchemaFrom}
	v := Verification{digest, p.Target, p.Grants, true, true, true}
	c := Checkpoint{digest, strings.Repeat("6", 64), p.Source, p.SchemaFrom, p.DataRevision, true}
	m := Migration{digest, c.Digest, p.Target, strings.Repeat("7", 64), p.SchemaTo, true}
	return p, b, v, c, m
}

func TestSharedPlan(t *testing.T) {
	path := os.Getenv("SPACE_P1_UPGRADE_PLAN")
	if path == "" {
		t.Skip("Нужен общий контракт из space-protocol")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(filepath.Join(filepath.Dir(path), "upgrade-plan.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := p.Digest()
	if err != nil || digest != strings.TrimSpace(string(expected)) {
		t.Fatal(digest, err)
	}
	t.Logf("plan_digest=%s steps=%d", digest, len(p.Steps))
}

func TestPlanValidation(t *testing.T) {
	cases := map[string]func(*Plan){
		"version": func(p *Plan) { p.Version = "v2" }, "space": func(p *Plan) { p.Space = "../other" }, "installation": func(p *Plan) { p.Installation = "" },
		"generation": func(p *Plan) { p.Generation = "old" }, "revision": func(p *Plan) { p.Revision = 0 }, "overflow": func(p *Plan) { p.Revision = lifecycle.MaxRevision - 1 },
		"data_revision": func(p *Plan) { p.DataRevision = 9007199254740992 }, "hash": func(p *Plan) { p.Source = "bad" }, "same_package": func(p *Plan) { p.Target = p.Source },
		"grants": func(p *Plan) { p.Grants = "" }, "downgrade": func(p *Plan) { p.SchemaTo = 0 }, "nil_steps": func(p *Plan) { p.Steps = nil },
		"missing_step": func(p *Plan) { p.Steps = p.Steps[:1] }, "gap": func(p *Plan) { p.Steps[1].From = 3 }, "order": func(p *Plan) { p.Steps[0], p.Steps[1] = p.Steps[1], p.Steps[0] },
		"duplicate_id": func(p *Plan) { p.Steps[1].ID = p.Steps[0].ID }, "step_hash": func(p *Plan) { p.Steps[0].Digest = "bad" }, "too_many": func(p *Plan) { p.Steps = make([]Step, 33) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p, _, _, _, _ := fixture()
			change(&p)
			if _, err := p.Digest(); err != ErrPlan {
				t.Fatal(err)
			}
		})
	}
	p, _, _, _, _ := fixture()
	p.SchemaTo = p.SchemaFrom
	p.Steps = []Step{}
	if _, err := p.Digest(); err != nil {
		t.Fatal("Обновление кода без смены схемы", err)
	}
}

func TestStrictDecode(t *testing.T) {
	p, _, _, _, _ := fixture()
	raw, _ := json.Marshal(p)
	for name, input := range map[string][]byte{
		"duplicate": append([]byte(`{"version":"space.upgrade.p1",`), raw[1:]...),
		"unknown":   append([]byte(`{"extra":true,`), raw[1:]...),
		"null":      bytes.Replace(raw, []byte(`"data_revision":19`), []byte(`"data_revision":null`), 1),
		"missing":   bytes.Replace(raw, []byte(`"data_revision":19,`), nil, 1),
		"case":      bytes.Replace(raw, []byte(`"version"`), []byte(`"Version"`), 1),
		"step_case": bytes.Replace(raw, []byte(`"id"`), []byte(`"ID"`), 1),
		"fraction":  bytes.Replace(raw, []byte(`"revision":7`), []byte(`"revision":7.5`), 1),
		"tail":      append(append([]byte{}, raw...), []byte(`{}`)...),
		"oversize":  []byte(strings.Repeat(" ", 17000)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(input); err != ErrPlan {
				t.Fatal(err)
			}
		})
	}
	var pretty bytes.Buffer
	json.Indent(&pretty, raw, "", "  ")
	decoded, err := Decode(pretty.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := p.Digest()
	b, _ := decoded.Digest()
	if a != b {
		t.Fatal("Пробелы изменили digest")
	}
}

func TestBoundEvidence(t *testing.T) {
	for _, part := range []string{"space", "installation", "generation", "revision", "package", "schema", "data", "verification_plan", "target", "grants", "trust", "compatibility", "approval", "checkpoint_plan", "checkpoint_digest", "checkpoint_source", "checkpoint_schema", "checkpoint_data", "checkpoint_complete"} {
		t.Run(part, func(t *testing.T) {
			p, b, v, c, _ := fixture()
			switch part {
			case "space":
				b.Space = "other"
			case "installation":
				b.Installation = "other"
			case "generation":
				b.Generation = strings.Repeat("b", 32)
			case "revision":
				b.Revision++
			case "package":
				b.Package = p.Target
			case "schema":
				b.Schema++
			case "data":
				b.DataRevision++
			case "verification_plan":
				v.Plan = strings.Repeat("0", 64)
			case "target":
				v.Target = p.Source
			case "grants":
				v.Grants = p.Source
			case "trust":
				v.Trusted = false
			case "compatibility":
				v.Compatible = false
			case "approval":
				v.Approved = false
			case "checkpoint_plan":
				c.Plan = strings.Repeat("0", 64)
			case "checkpoint_digest":
				c.Digest = strings.Repeat("9", 64)
			case "checkpoint_source":
				c.Source = p.Target
			case "checkpoint_schema":
				c.Schema++
			case "checkpoint_data":
				c.DataRevision++
			case "checkpoint_complete":
				c.Complete = false
			}
			if _, err := Begin(p, b, v, c, true); err != ErrEvidence {
				t.Fatal(err)
			}
		})
	}
	p, b, v, c, _ := fixture()
	if _, err := Begin(p, b, v, c, false); err != ErrAuthority {
		t.Fatal(err)
	}
}

func TestMigrationEvidence(t *testing.T) {
	for _, part := range []string{"plan", "checkpoint", "target", "schema", "result", "validated", "revoked_trust", "stale_generation", "stale_revision", "changed_step"} {
		t.Run(part, func(t *testing.T) {
			p, b, v, c, m := fixture()
			b.Revision++
			switch part {
			case "plan":
				m.Plan = "other"
			case "checkpoint":
				m.Checkpoint = strings.Repeat("9", 64)
			case "target":
				m.Target = p.Source
			case "schema":
				m.Schema = p.SchemaFrom
			case "result":
				m.Result = ""
			case "validated":
				m.Validated = false
			case "revoked_trust":
				v.Trusted = false
			case "stale_generation":
				b.Generation = "other"
			case "stale_revision":
				b.Revision--
			case "changed_step":
				p.Steps[0].Digest = strings.Repeat("9", 64)
			}
			if _, err := Finish(p, b, v, c, m); err != ErrEvidence {
				t.Fatal(err)
			}
		})
	}
}

func TestLifecycleIntegration(t *testing.T) {
	p, b, v, c, m := fixture()
	coordinator, _ := lifecycle.New("disabled", p.Revision)
	start, err := Begin(p, b, v, c, true)
	if err != nil {
		t.Fatal(err)
	}
	if d := coordinator.Apply(start); d.Code != "OK" || d.State != "upgrading" {
		t.Fatal(d)
	}
	if _, _, err := coordinator.Begin(context.Background()); err == nil {
		t.Fatal("Гость допущен во время обновления")
	}
	if d := coordinator.Apply(start); d.Code != "Aborted" {
		t.Fatal(d)
	}
	b.Revision++
	finish, err := Finish(p, b, v, c, m)
	if err != nil {
		t.Fatal(err)
	}
	if d := coordinator.Apply(finish); d.Code != "OK" || d.State != "disabled" || d.Admission {
		t.Fatal(d)
	}
	if d := coordinator.Apply(finish); d.Code != "Aborted" {
		t.Fatal(d)
	}
}

func TestActiveCallBlocksUpgrade(t *testing.T) {
	p, b, v, c, _ := fixture()
	coordinator, _ := lifecycle.New("enabled", p.Revision-2)
	_, release, err := coordinator.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	coordinator.Apply(lifecycle.Observation{Version: lifecycle.Version, Action: "disable", Actor: "owner", Authorized: true, ExpectedRevision: p.Revision - 2})
	finishDisable := lifecycle.Observation{Version: lifecycle.Version, Action: "finish_disable", Actor: "system", Authorized: true, ExpectedRevision: p.Revision - 1}
	if d := coordinator.Apply(finishDisable); d.Code != "FailedPrecondition" {
		t.Fatal(d)
	}
	start, err := Begin(p, b, v, c, true)
	if err != nil {
		t.Fatal(err)
	}
	if d := coordinator.Apply(start); d.Code == "OK" {
		t.Fatal("Принято обновление с активным вызовом")
	}
	release()
	if d := coordinator.Apply(finishDisable); d.Code != "OK" {
		t.Fatal(d)
	}
	if d := coordinator.Apply(start); d.Code != "OK" {
		t.Fatal(d)
	}
}
