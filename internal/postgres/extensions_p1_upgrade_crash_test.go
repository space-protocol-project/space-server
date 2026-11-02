//go:build linux

package postgres

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
)

// Отдельный процесс открывает реальную БД. Барьер после Advance имитирует
// потерю ответа после COMMIT; барьер внутри преобразования — незавершённый шаг.
func TestExtensionP1UpgradeProcess(t *testing.T) {
	address := os.Getenv("SPACE_P1_UPGRADE_DATABASE")
	if address == "" {
		return
	}
	ctx := context.Background()
	store, _, err := Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	host := &mutationProbe{run: func(context.Context) ([]byte, error) { return nil, nil }}
	s, err := store.NewExtensionP1ManagedService(ctx, host, mutationBindings("upgrade_test", []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	token := os.Getenv("SPACE_P1_UPGRADE_TOKEN")
	p, err := s.ReadUpgrade(ctx, token, "install")
	if err != nil {
		t.Fatal(err)
	}
	hold := func() {
		if err := os.WriteFile(os.Getenv("SPACE_P1_UPGRADE_BARRIER"), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		select {}
	}
	mode := os.Getenv("SPACE_P1_UPGRADE_MODE")
	if mode == "prepared" {
		hold()
	}
	v := upgrade.Verification{Plan: p.Digest, Target: p.Plan.Target, Grants: p.Plan.Grants, Trusted: true, Compatible: true, Approved: true}
	c := upgrade.Checkpoint{Plan: p.Digest, Digest: p.Plan.Checkpoint, Source: p.Plan.Source, Schema: p.Plan.SchemaFrom, DataRevision: p.Plan.DataRevision, Complete: true}
	_, err = s.AdvanceUpgrade(ctx, token, "install", p.Digest, p.NextStep, v, c, func(ctx context.Context, step upgrade.Step, input []byte) ([]byte, error) {
		if mode == "during_step" {
			hold()
		}
		return upgradeTestStep(ctx, step, input)
	})
	if err != nil {
		t.Fatal(err)
	}
	hold()
}

func TestExtensionP1UpgradeProcessRecovery(t *testing.T) {
	for _, mode := range []string{"prepared", "during_step", "after_commit"} {
		t.Run(mode, func(t *testing.T) {
			u := newUpgradeFixture(t)
			u.prepare(t)
			directory := t.TempDir()
			barrier := filepath.Join(directory, "barrier")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			log, err := os.Create(filepath.Join(directory, "process.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd := exec.Command(executable, "-test.run=^TestExtensionP1UpgradeProcess$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "SPACE_P1_UPGRADE_DATABASE="+u.f.url, "SPACE_P1_UPGRADE_TOKEN="+lifecycleToken(u.f.owner), "SPACE_P1_UPGRADE_MODE="+mode, "SPACE_P1_UPGRADE_BARRIER="+barrier)
			cmd.Stdout, cmd.Stderr = log, log
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			waitExtensionCrashFile(t, barrier)
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			store, _, err := Open(u.f.ctx, u.f.url)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			u.service = u.open(t, store)
			p, err := u.service.ReadUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install")
			expected := 0
			if mode == "after_commit" {
				expected = 1
			}
			if err != nil || p.NextStep != expected {
				t.Fatal(p, err)
			}
			calls := 0
			for !p.Ready() {
				p, err = u.advance(p.NextStep, func(ctx context.Context, s upgrade.Step, b []byte) ([]byte, error) {
					calls++
					return upgradeTestStep(ctx, s, b)
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2-expected || string(p.Document) != `{"schema":3}` {
				t.Fatal(calls, p)
			}
			var count int
			if err = u.f.store.pool.QueryRow(u.f.ctx, `SELECT count(*) FROM extension_p1_upgrade_steps`).Scan(&count); err != nil || count != 2 {
				t.Fatal(count, err)
			}
		})
	}
}
