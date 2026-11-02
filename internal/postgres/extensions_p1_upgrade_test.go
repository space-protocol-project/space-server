package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type upgradeFixture struct {
	f            *channelFixture
	service      *ExtensionP1ManagedService
	bindings     map[string]ExtensionP1MutationBinding
	plan         upgrade.Plan
	verification upgrade.Verification
	checkpoint   upgrade.Checkpoint
	digest       string
}

func newUpgradeFixture(t *testing.T) *upgradeFixture {
	t.Helper()
	f := newChannelFixture(t)
	channel := f.create(t, "upgrade_test")
	u := &upgradeFixture{f: f, bindings: mutationBindings(channel.Id, []byte{1})}
	u.service = u.open(t, f.store)
	var space, source string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT s.server_id,encode(i.package_hash,'hex') FROM server_state s CROSS JOIN extension_p1_instances i WHERE i.instance='demo'`).Scan(&space, &source); err != nil {
		t.Fatal(err)
	}
	u.plan = upgrade.Plan{Version: upgrade.Version, Space: space, Installation: "install", Generation: u.service.Generation(), Revision: 1, Source: source, Target: strings.Repeat("2", 64), Grants: strings.Repeat("3", 64), Checkpoint: strings.Repeat("6", 64), SchemaFrom: 1, SchemaTo: 3, Steps: []upgrade.Step{{ID: "first", From: 1, To: 2, Digest: strings.Repeat("4", 64)}, {ID: "second", From: 2, To: 3, Digest: strings.Repeat("5", 64)}}}
	u.digest, _ = u.plan.Digest()
	u.verification = upgrade.Verification{Plan: u.digest, Target: u.plan.Target, Grants: u.plan.Grants, Trusted: true, Compatible: true, Approved: true}
	u.checkpoint = upgrade.Checkpoint{Plan: u.digest, Digest: u.plan.Checkpoint, Source: source, Schema: 1, Complete: true}
	return u
}

func (u *upgradeFixture) open(t *testing.T, store *Store) *ExtensionP1ManagedService {
	t.Helper()
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	s, err := store.NewExtensionP1ManagedService(u.f.ctx, h, u.bindings)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (u *upgradeFixture) prepare(t *testing.T) {
	t.Helper()
	if err := u.service.PrepareUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "demo", u.plan, u.verification, u.checkpoint, []byte(`{"schema":1}`)); err != nil {
		t.Fatal(err)
	}
}

func upgradeTestStep(_ context.Context, step upgrade.Step, input []byte) ([]byte, error) {
	if step.ID == "first" && string(input) == `{"schema":1}` {
		return []byte(`{"schema":2}`), nil
	}
	if step.ID == "second" && string(input) == `{"schema":2}` {
		return []byte(`{"schema":3}`), nil
	}
	return nil, errors.New("Неверный порядок миграции")
}

func (u *upgradeFixture) advance(step int, execute ExtensionP1MigrationStep) (ExtensionP1UpgradeProgress, error) {
	return u.service.AdvanceUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install", u.digest, step, u.verification, u.checkpoint, execute)
}

func TestExtensionP1UpgradePersistentResume(t *testing.T) {
	u := newUpgradeFixture(t)
	u.prepare(t)
	if _, err := u.advance(0, upgradeTestStep); err != nil {
		t.Fatal(err)
	}
	original := u.service
	store, _, err := Open(u.f.ctx, u.f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	u.service = u.open(t, store)
	p, err := u.service.ReadUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install")
	if err != nil || p.NextStep != 1 || p.Plan.Generation != original.Generation() {
		t.Fatal(p, err)
	}
	_, err = original.ReadUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install")
	requireCode(t, err, codes.FailedPrecondition)
	attestations := 0
	p, err = u.service.ResumeUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install", func(_ context.Context, plan upgrade.Plan) (upgrade.Verification, upgrade.Checkpoint, error) {
		attestations++
		if plan.Generation != original.Generation() {
			t.Error("План изменён при восстановлении")
		}
		return u.verification, u.checkpoint, nil
	}, upgradeTestStep)
	if err != nil || !p.Ready() || string(p.Document) != `{"schema":3}` {
		t.Fatal(p, err)
	}
	if attestations != 1 {
		t.Fatal(attestations)
	}
	// Готовый результат не вызывает преобразователь и не включает установку.
	if _, err = u.advance(2, func(context.Context, upgrade.Step, []byte) ([]byte, error) {
		t.Error("Повтор выполненного шага")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"enable", "remove"} {
		_, err = u.service.Change(u.f.ctx, lifecycleToken(u.f.owner), u.service.Generation(), "install", action, 1)
		requireCode(t, err, codes.FailedPrecondition)
	}
	lifecycleRegistryState(t, u.f, "disabled", 1)
	mutationCounts(t, u.f, 0, 0, 0, 0)
	if err = CheckExtensionP1Backup(u.f.ctx, u.f.url, false); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionP1UpgradeRollback(t *testing.T) {
	for _, kind := range []string{"error", "invalid_json", "oversize", "sql_failure", "session_expiry"} {
		t.Run(kind, func(t *testing.T) {
			u := newUpgradeFixture(t)
			u.prepare(t)
			if kind == "session_expiry" {
				if _, err := u.f.store.pool.Exec(u.f.ctx, `UPDATE auth_sessions SET expires_at=clock_timestamp()+interval '700 milliseconds'`); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "sql_failure" {
				_, err := u.f.store.pool.Exec(u.f.ctx, `ALTER TABLE extension_p1_upgrade_steps ADD CONSTRAINT fail_step CHECK(ordinal<0)`)
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := u.advance(0, func(ctx context.Context, step upgrade.Step, input []byte) ([]byte, error) {
				switch kind {
				case "error":
					return nil, errors.New("Сбой преобразования")
				case "invalid_json":
					return []byte(`{"x":1,"x":2}`), nil
				case "oversize":
					return []byte(`"` + strings.Repeat("x", 16384) + `"`), nil
				case "session_expiry":
					time.Sleep(time.Second)
				}
				return upgradeTestStep(ctx, step, input)
			})
			if err == nil {
				t.Fatal("Принят незавершённый шаг")
			}
			var next, count int
			if err = u.f.store.pool.QueryRow(u.f.ctx, `SELECT next_step,(SELECT count(*) FROM extension_p1_upgrade_steps) FROM extension_p1_upgrade_jobs`).Scan(&next, &count); err != nil || next != 0 || count != 0 {
				t.Fatal(next, count, err)
			}
		})
	}
}

func TestExtensionP1UpgradeConcurrentStep(t *testing.T) {
	u := newUpgradeFixture(t)
	u.prepare(t)
	var calls, success atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := u.advance(0, func(ctx context.Context, s upgrade.Step, b []byte) ([]byte, error) {
				calls.Add(1)
				return upgradeTestStep(ctx, s, b)
			})
			if err == nil {
				success.Add(1)
			} else if status.Code(err) != codes.Aborted {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || success.Load() != 1 {
		t.Fatal(calls.Load(), success.Load())
	}
}

func TestExtensionP1UpgradeResumeBoundaries(t *testing.T) {
	t.Run("revalidate_each_step", func(t *testing.T) {
		u := newUpgradeFixture(t)
		u.prepare(t)
		calls := 0
		_, err := u.service.ResumeUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install", func(context.Context, upgrade.Plan) (upgrade.Verification, upgrade.Checkpoint, error) {
			calls++
			v := u.verification
			v.Trusted = calls == 1
			return v, u.checkpoint, nil
		}, upgradeTestStep)
		if err == nil || calls != 2 {
			t.Fatal(calls, err)
		}
		p, err := u.service.ReadUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install")
		if err != nil || p.NextStep != 1 {
			t.Fatal(p, err)
		}
	})
	t.Run("duplicate_plan", func(t *testing.T) {
		u := newUpgradeFixture(t)
		u.prepare(t)
		if err := u.service.PrepareUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "demo", u.plan, u.verification, u.checkpoint, []byte(`{"different":true}`)); err == nil {
			t.Fatal("План перезаписан")
		}
		p, err := u.service.ReadUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install")
		if err != nil || string(p.Document) != `{"schema":1}` {
			t.Fatal(p, err)
		}
	})
	t.Run("backup_generation", func(t *testing.T) {
		u := newUpgradeFixture(t)
		u.prepare(t)
		if _, err := u.advance(0, upgradeTestStep); err != nil {
			t.Fatal(err)
		}
		if err := CheckExtensionP1Backup(u.f.ctx, u.f.url, true); err != nil {
			t.Fatal(err)
		}
		if _, err := u.advance(1, upgradeTestStep); err == nil {
			t.Fatal("Старое поколение принято после восстановления")
		}
		store, _, err := Open(u.f.ctx, u.f.url)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		u.service = u.open(t, store)
		p, err := u.advance(1, upgradeTestStep)
		if err != nil || !p.Ready() {
			t.Fatal(p, err)
		}
	})
}

func TestExtensionP1UpgradePermissions(t *testing.T) {
	u := newUpgradeFixture(t)
	member, _ := u.f.enroll(t, true)
	err := u.service.PrepareUpgrade(u.f.ctx, lifecycleToken(member), "demo", u.plan, u.verification, u.checkpoint, []byte(`{}`))
	requireCode(t, err, codes.PermissionDenied)
	u.prepare(t)
	_, err = u.service.ReadUpgrade(u.f.ctx, "invalid", "install")
	requireCode(t, err, codes.Unauthenticated)
	_, err = u.service.AdvanceUpgrade(u.f.ctx, lifecycleToken(member), "install", u.digest, 0, u.verification, u.checkpoint, upgradeTestStep)
	requireCode(t, err, codes.PermissionDenied)
	u.verification.Trusted = false
	if _, err = u.advance(0, upgradeTestStep); err == nil {
		t.Fatal("Отозванное доверие принято")
	}
	u.verification.Trusted = true
	if _, err = u.advance(1, upgradeTestStep); status.Code(err) != codes.Aborted {
		t.Fatal(err)
	}
	if _, err = u.service.SystemTransition(u.f.ctx, "install", "quarantine", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = u.advance(0, upgradeTestStep); err == nil {
		t.Fatal("Миграция в карантине")
	}
	if err = CheckExtensionP1Backup(u.f.ctx, u.f.url, false); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionP1UpgradeCorruptJournal(t *testing.T) {
	for _, sql := range []string{
		`UPDATE extension_p1_upgrade_jobs SET digest=repeat('0',64)`,
		`UPDATE extension_p1_upgrade_jobs SET next_step=2`,
		`UPDATE extension_p1_upgrade_jobs SET document='{}'::bytea`,
		`UPDATE extension_p1_upgrade_steps SET input_digest=repeat('0',64)`,
		`DELETE FROM extension_p1_upgrade_steps`,
		`DROP TABLE extension_p1_upgrade_steps`,
		`ALTER TABLE extension_p1_upgrade_steps DROP CONSTRAINT extension_p1_upgrade_steps_installation_fkey; UPDATE extension_p1_upgrade_steps SET installation='orphan'`,
	} {
		t.Run(sql, func(t *testing.T) {
			u := newUpgradeFixture(t)
			u.prepare(t)
			if _, err := u.advance(0, upgradeTestStep); err != nil {
				t.Fatal(err)
			}
			if _, err := u.f.store.pool.Exec(u.f.ctx, sql); err != nil {
				t.Fatal(err)
			}
			if err := CheckExtensionP1Backup(u.f.ctx, u.f.url, false); err == nil {
				t.Fatal("Принята повреждённая копия")
			}
			store, _, err := Open(u.f.ctx, u.f.url)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			h := &mutationProbe{run: func(context.Context) ([]byte, error) { return nil, nil }}
			if _, err = store.NewExtensionP1ManagedService(u.f.ctx, h, u.bindings); err == nil {
				t.Fatal("Принят повреждённый журнал")
			}
		})
	}
}
