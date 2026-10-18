package voice

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRoomCapacityExpiryAndScope(t *testing.T) {
	now := time.Now()
	ctx := context.Background()
	r := NewRooms(func(context.Context, Actor, string, Action) error { return nil }, func() time.Time { return now })
	for i := 0; i < 32; i++ {
		if _, err := r.Join(ctx, Actor{"s", "p", fmt.Sprint(i)}, "room"); err != nil {
			t.Fatal(err)
		}
	}
	a := Actor{"s", "p", "extra"}
	if _, err := r.Join(ctx, a, "room"); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
	other, err := r.Join(ctx, a, "other-room")
	if err != nil {
		t.Fatal(err)
	}
	items, _ := r.Snapshot(ctx, Actor{"other-server", "p", "extra"}, "room")
	if len(items) != 0 {
		t.Fatal("участники другого сервера раскрыты")
	}
	items, _ = r.Snapshot(ctx, a, "other-room")
	if len(items) != 1 {
		t.Fatal(len(items))
	}
	now = now.Add(30 * time.Second)
	if _, err := r.Renew(ctx, a, other.SessionID); !errors.Is(err, ErrMissing) {
		t.Fatal("просроченный сеанс продлён", err)
	}
	if _, err := r.Join(ctx, a, "room"); err != nil {
		t.Fatal("ёмкость не освободилась", err)
	}
}

func TestRoomLifecycleAndIsolation(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	r := NewRooms(func(context.Context, Actor, string, Action) error { return nil }, func() time.Time { return now })
	a := Actor{"server", "alice", "device1"}
	x, err := r.Join(ctx, a, "room")
	if err != nil || !x.Muted {
		t.Fatalf("join: %v %#v", err, x)
	}
	again, _ := r.Join(ctx, a, "room")
	if again.SessionID != x.SessionID {
		t.Fatal("повтор создал дубль")
	}
	b := Actor{"server", "alice", "device2"}
	y, _ := r.Join(ctx, b, "room")
	if y.SessionID == x.SessionID {
		t.Fatal("устройства смешаны")
	}
	foreign := Actor{"other-server", "alice", "device1"}
	if err := r.SetMuted(ctx, foreign, x.SessionID, false); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
	r.Leave(foreign, x.SessionID)
	items, _ := r.Snapshot(ctx, a, "room")
	if len(items) != 2 {
		t.Fatal(len(items))
	}
	items[0].Muted = false
	items, _ = r.Snapshot(ctx, a, "room")
	for _, item := range items {
		if !item.Muted {
			t.Fatal("снимок изменил реестр")
		}
	}
	if err := r.SetMuted(ctx, a, x.SessionID, false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * time.Second)
	if _, err := r.Renew(ctx, a, x.SessionID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Second)
	items, _ = r.Snapshot(ctx, a, "room")
	if len(items) != 1 || items[0].SessionID != x.SessionID {
		t.Fatal("не очищено пропавшее устройство")
	}
	r.Leave(a, x.SessionID)
	r.Leave(a, x.SessionID)
	items, _ = r.Snapshot(ctx, a, "room")
	if len(items) != 0 {
		t.Fatal("выход не очистил комнату")
	}
}

func TestRightsAreCheckedAgain(t *testing.T) {
	listen, publish := true, true
	r := NewRooms(func(_ context.Context, _ Actor, _ string, action Action) error {
		if action == Listen && listen || action == Publish && publish {
			return nil
		}
		return ErrDenied
	}, nil)
	ctx := context.Background()
	a := Actor{"s", "p", "d"}
	x, _ := r.Join(ctx, a, "room")
	if err := r.SetMuted(ctx, a, x.SessionID, false); err != nil {
		t.Fatal(err)
	}
	publish = false
	x, err := r.Renew(ctx, a, x.SessionID)
	if err != nil || !x.Muted {
		t.Fatal("отзыв публикации не выключил микрофон")
	}
	if err := r.SetMuted(ctx, a, x.SessionID, false); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	listen = false
	if _, err := r.Renew(ctx, a, x.SessionID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	listen = true
	items, _ := r.Snapshot(ctx, a, "room")
	if len(items) != 0 {
		t.Fatal("отозванное подключение осталось")
	}
}

func TestFailClosedAndConcurrentJoin(t *testing.T) {
	ctx := context.Background()
	a := Actor{"s", "p", "d"}
	if _, err := NewRooms(nil, nil).Join(ctx, a, "room"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	r := NewRooms(func(context.Context, Actor, string, Action) error { return nil }, nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Join(ctx, a, "room"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	items, _ := r.Snapshot(ctx, a, "room")
	if len(items) != 1 {
		t.Fatal("конкурентный дубль", len(items))
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.Join(cancelled, a, "new"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := r.Join(ctx, Actor{}, "room"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
