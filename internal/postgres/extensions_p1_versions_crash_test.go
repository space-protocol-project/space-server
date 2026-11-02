//go:build linux

package postgres

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"google.golang.org/grpc/codes"
)

func versionProxy(t *testing.T, p *publishFixture, mode string) *extensionDatabaseProxy {
	t.Helper()
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
	t.Cleanup(store.Close)
	u.service, err = store.NewExtensionP1ManagedService(u.f.ctx, p.host, u.bindings)
	if err != nil {
		t.Fatal(err)
	}
	return proxy
}

func waitVersionCommit(t *testing.T, p *publishFixture, proxy *extensionDatabaseProxy) {
	t.Helper()
	select {
	case evidence := <-proxy.hit:
		t.Log(evidence)
	case <-time.After(time.Second):
		t.Fatal("COMMIT не перехвачен")
	}
	_, err := p.u.service.ReadUpgrade(p.u.f.ctx, lifecycleToken(p.u.f.owner), "install")
	requireCode(t, err, codes.Unavailable)
}

func TestExtensionP1VersionsPrepareRecovery(t *testing.T) {
	for _, mode := range []string{"versions_prepare_before_commit", "versions_prepare_after_commit"} {
		t.Run(mode, func(t *testing.T) {
			p := firstVersion(t)
			old := p.u.digest
			proxy := versionProxy(t, p, mode)
			nextVersion(t, p, false)
			u := p.u
			proxy.armed.Store(true)
			if err := u.service.PrepareWASMUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "demo", u.plan, u.verification, u.checkpoint, p.target, p.modules); err == nil {
				t.Fatal("Подтверждён оборванный COMMIT подготовки")
			}
			waitVersionCommit(t, p, proxy)
			p.reopen(t)
			versionList(t, p, 1)
			progress, err := u.service.ReadUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "versions_prepare_before_commit" {
				if !progress.Published || progress.Digest != old {
					t.Fatal(progress)
				}
				// Откатившийся план не был сохранён: новый план привязывается к новому поколению.
				nextVersion(t, p, false)
				p.prepare(t)
			} else if progress.Published || progress.Digest != u.digest {
				t.Fatal(progress)
			}
			if _, err = p.resume(); err != nil {
				t.Fatal(err)
			}
			if err = p.publish(); err != nil {
				t.Fatal(err)
			}
			p.reopen(t)
			versionList(t, p, 2)
		})
	}
}

func TestExtensionP1VersionsCommitRecovery(t *testing.T) {
	for _, mode := range []string{"publish_before_commit", "publish_after_commit"} {
		t.Run(mode, func(t *testing.T) {
			p := firstVersion(t)
			nextVersion(t, p, false)
			p.prepare(t)
			if _, err := p.resume(); err != nil {
				t.Fatal(err)
			}
			proxy := versionProxy(t, p, mode)
			proxy.armed.Store(true)
			if err := p.publish(); err == nil {
				t.Fatal("Подтверждён оборванный COMMIT публикации")
			}
			waitVersionCommit(t, p, proxy)
			p.reopen(t)
			want := 1
			if mode == "publish_after_commit" {
				want = 2
			}
			versionList(t, p, want)
			if err := p.publish(); err != nil {
				t.Fatal(err)
			}
			p.reopen(t)
			versionList(t, p, 2)
			if err := CheckExtensionP1Backup(p.u.f.ctx, p.u.f.url, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExtensionP1VersionsProcessRecovery(t *testing.T) {
	for _, mode := range []string{"before_publish", "after_publish"} {
		t.Run(mode, func(t *testing.T) {
			p := firstVersion(t)
			nextVersion(t, p, false)
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
			p.reopen(t)
			want := 1
			if mode == "after_publish" {
				want = 2
			}
			versionList(t, p, want)
			if err = p.publish(); err != nil {
				t.Fatal(err)
			}
			p.reopen(t)
			list := versionList(t, p, 2)
			managedChange(t, u.f, u.service, "enable", list[1].Revision)
			api := extensionP1RPCClient(t, u.f, u.service)
			if _, err = api.QueryExtension(u.f.owner, extensionP1Query()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
