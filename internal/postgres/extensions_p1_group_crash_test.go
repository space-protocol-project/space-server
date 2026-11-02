//go:build linux

package postgres

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"google.golang.org/grpc/codes"
)

func groupProxy(t *testing.T, g *groupFixture, mode string) *extensionDatabaseProxy {
	t.Helper()
	proxy, database := newExtensionDatabaseProxy(t, g.f.url, mode)
	address, err := url.Parse(database)
	if err != nil {
		t.Fatal(err)
	}
	options := address.Query()
	options.Set("default_query_exec_mode", "simple_protocol")
	address.RawQuery = options.Encode()
	store, _, err := Open(g.f.ctx, address.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	g.service, err = store.NewExtensionP1ManagedService(g.f.ctx, g.host, g.bindings)
	if err != nil {
		t.Fatal(err)
	}
	return proxy
}

func TestExtensionP1GroupCommitRecovery(t *testing.T) {
	for _, stage := range []string{"prepare", "step", "publish"} {
		for _, point := range []string{"before_commit", "after_commit"} {
			t.Run(stage+"_"+point, func(t *testing.T) {
				g := newGroupFixture(t)
				proxy := groupProxy(t, g, "group_"+stage+"_"+point)
				g.plan(t, 1)
				if stage != "prepare" {
					if err := g.prepare(); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "publish" {
					for _, id := range []string{"demo", "other"} {
						if err := g.advance(id); err != nil {
							t.Fatal(err)
						}
					}
				}
				proxy.armed.Store(true)
				var err error
				switch stage {
				case "prepare":
					err = g.prepare()
				case "step":
					err = g.advance("demo")
				case "publish":
					err = g.publish()
				}
				if err == nil {
					t.Fatal("Подтверждена оборванная транзакция")
				}
				select {
				case evidence := <-proxy.hit:
					t.Log(evidence)
				case <-time.After(time.Second):
					t.Fatal("COMMIT не перехвачен")
				}
				_, err = g.service.ReadGroupUpgrade(g.f.ctx, lifecycleToken(g.f.owner), "install")
				requireCode(t, err, codes.Unavailable)
				g.reopen(t)
				if stage == "prepare" && point == "before_commit" {
					g.plan(t, 1)
					if err = g.prepare(); err != nil {
						t.Fatal(err)
					}
				}
				p := g.read(t)
				wantPublished := stage == "publish" && point == "after_commit"
				if p.Published != wantPublished {
					t.Fatal(p)
				}
				if stage == "step" {
					want := 0
					if point == "after_commit" {
						want = 1
					}
					if p.NextSteps["demo"] != want || p.NextSteps["other"] != 0 {
						t.Fatal(p)
					}
				}
				g.state(t, wantPublished)
				for _, id := range []string{"demo", "other"} {
					if p.NextSteps[id] == 0 {
						if err = g.advance(id); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err = g.publish(); err != nil {
					t.Fatal(err)
				}
				g.reopen(t)
				g.state(t, true)
			})
		}
	}
}

type groupCrashConfig struct {
	Database, Token, Barrier, Mode string
	Bindings                       map[string]ExtensionP1MutationBinding
	Members                        []ExtensionP1GroupMember
	Evidence                       map[string]ExtensionP1GroupEvidence
	Digest                         string
}

func TestExtensionP1GroupProcess(t *testing.T) {
	file := os.Getenv("SPACE_P1_GROUP_PROCESS")
	if file == "" {
		return
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var c groupCrashConfig
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), os.Getenv("SPACE_P1_WORKER"))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	store, _, err := Open(ctx, c.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s, err := store.NewExtensionP1ManagedService(ctx, host, c.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode == "between_instances" {
		_, err = s.AdvanceGroupUpgrade(ctx, c.Token, "install", c.Digest, "demo", 0, host, c.Evidence["demo"])
	} else if c.Mode == "after_publish" {
		err = s.PublishGroupUpgrade(ctx, c.Token, "install", c.Digest, c.Evidence)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(c.Barrier, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestExtensionP1GroupProcessRecovery(t *testing.T) {
	for _, mode := range []string{"between_instances", "before_publish", "after_publish"} {
		t.Run(mode, func(t *testing.T) {
			g := newGroupFixture(t)
			if err := g.prepare(); err != nil {
				t.Fatal(err)
			}
			if mode != "between_instances" {
				for _, id := range []string{"demo", "other"} {
					if err := g.advance(id); err != nil {
						t.Fatal(err)
					}
				}
			}
			dir := t.TempDir()
			c := groupCrashConfig{Database: g.f.url, Token: lifecycleToken(g.f.owner), Barrier: filepath.Join(dir, "barrier"), Mode: mode, Bindings: g.bindings, Members: g.members, Evidence: g.evidence, Digest: g.digest}
			raw, _ := json.Marshal(c)
			file := filepath.Join(dir, "config.json")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=^TestExtensionP1GroupProcess$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "SPACE_P1_GROUP_PROCESS="+file)
			log, err := os.Create(filepath.Join(dir, "process.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd.Stdout, cmd.Stderr = log, log
			if err = g.host.Close(); err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			waitExtensionCrashFile(t, c.Barrier)
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			g.host, err = extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), os.Getenv("SPACE_P1_WORKER"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = g.host.Close() })
			g.reopen(t)
			p := g.read(t)
			if p.Published != (mode == "after_publish") {
				t.Fatal(p)
			}
			if mode == "between_instances" && (p.NextSteps["demo"] != 1 || p.NextSteps["other"] != 0) {
				t.Fatal(p)
			}
			g.state(t, mode == "after_publish")
			if mode == "between_instances" {
				if err = g.advance("other"); err != nil {
					t.Fatal(err)
				}
			}
			if err = g.publish(); err != nil {
				t.Fatal(err)
			}
			g.reopen(t)
			g.state(t, true)
		})
	}
}

func TestExtensionP1GroupCorruption(t *testing.T) {
	for _, sql := range []string{
		`UPDATE extension_p1_group_jobs SET record_hash=repeat('0',64)`,
		`UPDATE extension_p1_group_versions SET record_hash=repeat('0',64)`,
		`DELETE FROM extension_p1_group_versions`,
		`DELETE FROM extension_p1_group_jobs`,
		`UPDATE extension_p1_group_versions SET principal='wrong'`,
		`DROP TABLE extension_p1_group_versions`,
		`UPDATE extension_p1_instances SET package_hash=decode(repeat('0',64),'hex') WHERE instance='other'`,
	} {
		t.Run(strings.Fields(sql)[0]+"_"+strings.Fields(sql)[1], func(t *testing.T) {
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
			if _, err := g.f.store.pool.Exec(g.f.ctx, sql); err != nil {
				t.Fatal(err)
			}
			if err := CheckExtensionP1Backup(g.f.ctx, g.f.url, false); err == nil {
				t.Fatal("Принята повреждённая копия")
			}
			store, _, err := Open(g.f.ctx, g.f.url)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, err = store.NewExtensionP1ManagedService(g.f.ctx, g.host, g.bindings); err == nil {
				t.Fatal("Открыт повреждённый реестр")
			}
		})
	}
}
