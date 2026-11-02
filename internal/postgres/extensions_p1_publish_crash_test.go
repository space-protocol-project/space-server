//go:build linux

package postgres

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
	"google.golang.org/grpc/codes"
)

func TestExtensionP1PublishCommitRecovery(t *testing.T) {
	for _, mode := range []string{"publish_before_commit", "publish_after_commit"} {
		t.Run(mode, func(t *testing.T) {
			p := newPublishFixture(t, "ok")
			p.prepare(t)
			if _, err := p.resume(); err != nil {
				t.Fatal(err)
			}
			u := p.u
			proxy, database := newExtensionDatabaseProxy(t, u.f.url, mode)
			address, err := url.Parse(database)
			if err != nil {
				t.Fatal(err)
			}
			options := address.Query()
			options.Set("default_query_exec_mode", "simple_protocol")
			address.RawQuery = options.Encode()
			store, _, err := Open(u.f.ctx, address.String())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			u.service, err = store.NewExtensionP1ManagedService(u.f.ctx, p.host, u.bindings)
			if err != nil {
				t.Fatal(err)
			}
			proxy.armed.Store(true)
			if err = p.publish(); err == nil {
				t.Fatal("Подтверждён оборванный COMMIT")
			}
			select {
			case evidence := <-proxy.hit:
				t.Log(evidence)
			case <-time.After(time.Second):
				t.Fatal("COMMIT не перехвачен")
			}
			_, err = u.service.ReadUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install")
			requireCode(t, err, codes.Unavailable)
			published := mode == "publish_after_commit"
			p.state(t, published)
			p.reopen(t)
			result, err := u.service.ReadUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install")
			if err != nil || result.Published != published {
				t.Fatal(result, err)
			}
			if err = p.publish(); err != nil {
				t.Fatal(err)
			}
			p.state(t, true)
			if err = CheckExtensionP1Backup(u.f.ctx, u.f.url, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExtensionP1PublishProcess(t *testing.T) {
	database := os.Getenv("SPACE_P1_PUBLISH_DATABASE")
	if database == "" {
		return
	}
	ctx := context.Background()
	store, _, err := Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), os.Getenv("SPACE_P1_WORKER"))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	s, err := store.NewExtensionP1ManagedService(ctx, host, mutationBindings("publish_test", publishModule(t, "mutation")))
	if err != nil {
		t.Fatal(err)
	}
	token := os.Getenv("SPACE_P1_PUBLISH_TOKEN")
	p, err := s.ReadUpgrade(ctx, token, "install")
	if err != nil {
		t.Fatal(err)
	}
	hold := func() {
		if err := os.WriteFile(os.Getenv("SPACE_P1_PUBLISH_BARRIER"), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		select {}
	}
	if os.Getenv("SPACE_P1_PUBLISH_MODE") == "before_publish" {
		hold()
	}
	v := upgrade.Verification{Plan: p.Digest, Target: p.Plan.Target, Grants: p.Plan.Grants, Trusted: true, Compatible: true, Approved: true}
	c := upgrade.Checkpoint{Plan: p.Digest, Digest: p.Plan.Checkpoint, Source: p.Plan.Source, Schema: p.Plan.SchemaFrom, DataRevision: p.Plan.DataRevision, Complete: true}
	if err = s.PublishUpgrade(ctx, token, "install", p.Digest, v, c); err != nil {
		t.Fatal(err)
	}
	hold()
}

func TestExtensionP1PublishProcessRecovery(t *testing.T) {
	for _, mode := range []string{"before_publish", "after_publish"} {
		t.Run(mode, func(t *testing.T) {
			p := newPublishFixture(t, "ok")
			p.prepare(t)
			if _, err := p.resume(); err != nil {
				t.Fatal(err)
			}
			u := p.u
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
			cmd := exec.Command(executable, "-test.run=^TestExtensionP1PublishProcess$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "SPACE_P1_PUBLISH_DATABASE="+u.f.url, "SPACE_P1_PUBLISH_TOKEN="+lifecycleToken(u.f.owner), "SPACE_P1_PUBLISH_MODE="+mode, "SPACE_P1_PUBLISH_BARRIER="+barrier)
			cmd.Stdout, cmd.Stderr = log, log
			if err = p.host.Close(); err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			waitExtensionCrashFile(t, barrier)
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			p.host, err = extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), os.Getenv("SPACE_P1_WORKER"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := p.host.Close(); err != nil {
					t.Error(err)
				}
			})
			p.state(t, mode == "after_publish")
			p.reopen(t)
			if err = p.publish(); err != nil {
				t.Fatal(err)
			}
			p.state(t, true)
		})
	}
}
