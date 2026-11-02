//go:build linux

package postgres

import (
	"net/url"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
)

func TestExtensionP1UpgradeUncertainCommit(t *testing.T) {
	for _, mode := range []string{"upgrade_before_commit", "upgrade_after_commit"} {
		t.Run(mode, func(t *testing.T) {
			u := newUpgradeFixture(t)
			u.prepare(t)
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
			u.service = u.open(t, store)
			proxy.armed.Store(true)
			if _, err = u.advance(0, upgradeTestStep); err == nil {
				t.Fatal("Получено подтверждение оборванного COMMIT")
			}
			select {
			case evidence := <-proxy.hit:
				t.Log(evidence)
			case <-time.After(time.Second):
				t.Fatal("COMMIT не перехвачен")
			}
			_, err = u.service.ReadUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install")
			requireCode(t, err, codes.Unavailable)
			nextStore, _, err := Open(u.f.ctx, u.f.url)
			if err != nil {
				t.Fatal(err)
			}
			defer nextStore.Close()
			u.service = u.open(t, nextStore)
			p, err := u.service.ReadUpgrade(u.f.ctx, lifecycleToken(u.f.owner), "install")
			want := 0
			if mode == "upgrade_after_commit" {
				want = 1
			}
			if err != nil || p.NextStep != want {
				t.Fatal(p, err)
			}
			for !p.Ready() {
				p, err = u.advance(p.NextStep, upgradeTestStep)
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
