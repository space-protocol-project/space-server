package demo

import (
	"context"
	"errors"
	"testing"

	"github.com/space-protocol-project/space-server/experiments/extensions/sdk"
)

func contributionRegistration(t *testing.T) Registration {
	t.Helper()
	p, err := sdk.ContributionStarter()
	if err != nil {
		t.Fatal(err)
	}
	r, err := FromManifest(p["manifest.json"], p["server.wasm"], map[string][]byte{"schemas/empty.json": p["schemas/empty.json"]})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestContributionsRegistry(t *testing.T) {
	r := contributionRegistration(t)
	h := &fakeHost{}
	d, err := New(h, grant, map[string]Registration{"demo": r})
	if err != nil {
		t.Fatal(err)
	}
	items, digest, err := d.Contributions(context.Background(), "", "demo")
	if err != nil || len(items) != 2 || items[0].ID != "details" || items[1].Kind != "overview" || digest == [32]byte{} || h.calls != 0 {
		t.Fatal(items, digest, err, h.calls)
	}
	items[0].Title["en"] = "Changed"
	r.SourceManifest[0] = '!'
	r.SourceSchemas["schemas/empty.json"][0] = '!'
	again, same, err := d.Contributions(context.Background(), "", "demo")
	if err != nil || same != digest || again[0].Title["en"] != "Details" {
		t.Fatal(again, same, err)
	}
	for _, op := range []struct{ kind, name string }{{"query", "summary"}, {"query", "details"}, {"command", "refresh"}} {
		q := request()
		q.Kind, q.Name = op.kind, op.name
		if _, err = d.Dispatch(context.Background(), "", q); err != nil {
			t.Fatal(op, err)
		}
	}
	q := request()
	q.Name = "undeclared"
	if _, err = d.Dispatch(context.Background(), "", q); !errors.Is(err, ErrRequest) || h.calls != 3 {
		t.Fatal(err, h.calls)
	}
}

func TestContributionsFence(t *testing.T) {
	h := &fakeHost{}
	count := 0
	authorize := func(ctx context.Context, token, instance string, write bool) (Principal, error) {
		if write {
			t.Fatal("Чтение реестра требует write")
		}
		p, err := grant(ctx, token, instance, write)
		count++
		if count == 2 {
			p.AccessStamp = "changed"
		}
		return p, err
	}
	d, err := New(h, authorize, map[string]Registration{"demo": contributionRegistration(t)})
	if err != nil {
		t.Fatal(err)
	}
	items, digest, err := d.Contributions(context.Background(), "", "demo")
	if !errors.Is(err, ErrDenied) || items != nil || digest != [32]byte{} || h.calls != 0 {
		t.Fatal(items, digest, err, h.calls)
	}
}

func TestContributionsRequireSchemas(t *testing.T) {
	r := contributionRegistration(t)
	r.SourceSchemas = nil
	if _, err := New(&fakeHost{}, grant, map[string]Registration{"demo": r}); err == nil {
		t.Fatal("Пропущена проверка схем")
	}
}
