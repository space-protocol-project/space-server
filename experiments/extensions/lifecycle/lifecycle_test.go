package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
)

func observation(action, actor string, revision int64) Observation {
	return Observation{Version: Version, Action: action, Actor: actor, Authorized: true, ExpectedRevision: revision, PackageVerified: true, Compatible: true, GrantsApproved: true, BackupReady: true, MigrationReady: true}
}

func TestSharedContract(t *testing.T) {
	path := os.Getenv("SPACE_P1_LIFECYCLE_CASES")
	if path == "" {
		t.Skip("Общий контракт предоставляет GitHub CI")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Version string `json:"version"`
		Cases   []struct {
			Name     string      `json:"name"`
			Input    Observation `json:"input"`
			Expected Decision    `json:"expected"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || fixture.Version != Version || len(fixture.Cases) == 0 {
		t.Fatal("Неверный общий контракт", err)
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := Decide(c.Input); got != c.Expected {
				t.Fatalf("got=%+v want=%+v", got, c.Expected)
			}
		})
	}
}

func TestInvalidObservations(t *testing.T) {
	base := observation("disable", "owner", 1)
	base.State, base.Revision = "enabled", 1
	for _, mutate := range []func(*Observation){
		func(o *Observation) { o.Version = "v99" },
		func(o *Observation) { o.State = "unknown" },
		func(o *Observation) { o.Action = "purge" },
		func(o *Observation) { o.Active = -1 },
		func(o *Observation) { o.Active = 129 },
		func(o *Observation) { o.Revision = 0 },
		func(o *Observation) { o.ExpectedRevision = MaxRevision + 1 },
	} {
		o := base
		mutate(&o)
		if d := Decide(o); d.Code != "InvalidArgument" || !d.PreserveData || d.CancelActive || d.Admission {
			t.Fatal(d)
		}
	}
}

func coordinator(t *testing.T) *Coordinator {
	t.Helper()
	c, err := New("enabled", 1)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDisableDrain(t *testing.T) {
	c := coordinator(t)
	ctx, release, err := c.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if d := c.Apply(observation("disable", "owner", 1)); d.Code != "OK" || d.State != "disabling" || ctx.Err() != context.Canceled {
		t.Fatal(d, ctx.Err())
	}
	if _, _, err := c.Begin(context.Background()); err == nil {
		t.Fatal("Новый вызов допущен")
	}
	if d := c.Apply(observation("finish_disable", "system", 2)); d.Code != "FailedPrecondition" {
		t.Fatal(d)
	}
	release()
	release()
	if d := c.Apply(observation("finish_disable", "system", 2)); d.Code != "OK" || d.Revision != 3 {
		t.Fatal(d)
	}
	if d := c.Apply(observation("enable", "owner", 3)); d.Code != "OK" {
		t.Fatal(d)
	}
}

func TestConcurrentAdmissionAndQuarantine(t *testing.T) {
	c := coordinator(t)
	first, releaseFirst, err := c.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFirst()
	var wg sync.WaitGroup
	start, inspect := make(chan struct{}), make(chan struct{})
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ctx, release, err := c.Begin(context.Background())
			if err != nil {
				return
			}
			defer release()
			<-inspect
			if ctx.Err() != context.Canceled {
				t.Error("Активный вызов не отменён")
			}
		}()
	}
	close(start)
	d := c.Apply(observation("quarantine", "system", 1))
	close(inspect)
	wg.Wait()
	if d.Code != "OK" || d.State != "quarantined" {
		t.Fatal(d)
	}
	if first.Err() != context.Canceled {
		t.Fatal("Карантин не отменил исходный вызов")
	}
	releaseFirst()
	if _, _, err := c.Begin(context.Background()); err == nil {
		t.Fatal("Карантин пропустил вызов")
	}
	if d := c.Apply(observation("remove", "owner", 2)); d.Code != "OK" || !d.PreserveData {
		t.Fatal(d)
	}
}

func TestConcurrentRevision(t *testing.T) {
	c := coordinator(t)
	results := make(chan Decision, 2)
	for range 2 {
		go func() { results <- c.Apply(observation("disable", "owner", 1)) }()
	}
	counts := map[string]int{}
	for range 2 {
		counts[(<-results).Code]++
	}
	if counts["OK"] != 1 || counts["Aborted"] != 1 {
		t.Fatal(counts)
	}
}

func TestDeniedTransitionDoesNotCancel(t *testing.T) {
	c := coordinator(t)
	ctx, release, err := c.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	o := observation("disable", "owner", 1)
	o.Authorized = false
	if d := c.Apply(o); d.Code != "PermissionDenied" || ctx.Err() != nil {
		t.Fatal(d, ctx.Err())
	}
	_, second, err := c.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second()
}

func TestQuotaAndCancelledParent(t *testing.T) {
	c := coordinator(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Begin(ctx); err == nil {
		t.Fatal("Отменённый вызов допущен")
	}
	for range 128 {
		_, release, err := c.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	if _, _, err := c.Begin(context.Background()); err == nil {
		t.Fatal("Превышена квота")
	}
}
