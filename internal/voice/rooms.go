// Package voice содержит управление присутствием, но пока не передаёт звук.
package voice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

var (
	ErrDenied  = errors.New("Нет доступа к голосовой комнате")
	ErrMissing = errors.New("Голосовое подключение завершено")
	ErrFull    = errors.New("Голосовая комната заполнена")
	ErrInvalid = errors.New("Некорректное голосовое подключение")
)

type Action string

const (
	Listen  Action = "listen"
	Publish Action = "publish"
)

// Actor должен поступать из проверенной сервером сессии, а не тела RPC.
type Actor struct{ ServerID, PrincipalID, DeviceID string }
type Authorize func(context.Context, Actor, string, Action) error
type Participant struct {
	SessionID string
	Actor     Actor
	RoomID    string
	Muted     bool
	ExpiresAt time.Time
}

// Rooms — ограниченный реестр текущего процесса. После перезапуска все должны
// войти заново. Адаптер медиа обязан закрывать удалённые сеансы самостоятельно.
// Пока реестр не подключён к публичному RPC и не выдаёт медиа-допуски.
type Rooms struct {
	mu           sync.Mutex
	authorize    Authorize
	clock        func() time.Time
	participants map[string]Participant
}

func NewRooms(authorize Authorize, clock func() time.Time) *Rooms {
	if clock == nil {
		clock = time.Now
	}
	return &Rooms{authorize: authorize, clock: clock, participants: make(map[string]Participant)}
}

func valid(actor Actor, room string) bool {
	return actor.ServerID != "" && actor.PrincipalID != "" && actor.DeviceID != "" && len(room) > 0 && len(room) <= 256
}

func (r *Rooms) allow(ctx context.Context, actor Actor, room string, action Action) error {
	if !valid(actor, room) {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.authorize == nil {
		return ErrDenied
	}
	return r.authorize(ctx, actor, room, action)
}

// Join повторяется для того же устройства без создания дубля. Новое устройство
// той же личности остаётся отдельным подключением. Микрофон всегда выключен.
func (r *Rooms) Join(ctx context.Context, actor Actor, room string) (Participant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire()
	if err := r.allow(ctx, actor, room, Listen); err != nil {
		return Participant{}, err
	}
	var count int
	for _, item := range r.participants {
		if item.Actor == actor && item.RoomID == room {
			return item, nil
		}
		if item.Actor.ServerID == actor.ServerID && item.RoomID == room {
			count++
		}
	}
	if count >= 32 || len(r.participants) >= 1024 {
		return Participant{}, ErrFull
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Participant{}, err
	}
	item := Participant{hex.EncodeToString(nonce[:]), actor, room, true, r.clock().Add(30 * time.Second)}
	r.participants[item.SessionID] = item
	return item, nil
}

func (r *Rooms) owned(actor Actor, session string) (Participant, error) {
	item, ok := r.participants[session]
	if !ok || item.Actor != actor {
		return Participant{}, ErrMissing
	}
	return item, nil
}

// Renew перепроверяет доступ. Потеря права слушать удаляет подключение;
// потеря права говорить принудительно выключает микрофон.
func (r *Rooms) Renew(ctx context.Context, actor Actor, session string) (Participant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire()
	item, err := r.owned(actor, session)
	if err != nil {
		return Participant{}, err
	}
	if err := r.allow(ctx, actor, item.RoomID, Listen); err != nil {
		delete(r.participants, session)
		return Participant{}, err
	}
	if !item.Muted && r.allow(ctx, actor, item.RoomID, Publish) != nil {
		item.Muted = true
	}
	item.ExpiresAt = r.clock().Add(30 * time.Second)
	r.participants[session] = item
	return item, nil
}

func (r *Rooms) SetMuted(ctx context.Context, actor Actor, session string, muted bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire()
	item, err := r.owned(actor, session)
	if err != nil {
		return err
	}
	if err := r.allow(ctx, actor, item.RoomID, Listen); err != nil {
		return err
	}
	if !muted {
		if err := r.allow(ctx, actor, item.RoomID, Publish); err != nil {
			return err
		}
	}
	item.Muted = muted
	r.participants[session] = item
	return nil
}

// Leave идемпотентен и не требует прежнего допуска: выход доступен и после отзыва.
func (r *Rooms) Leave(actor Actor, session string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.owned(actor, session); err == nil {
		delete(r.participants, session)
	}
}

func (r *Rooms) Snapshot(ctx context.Context, actor Actor, room string) ([]Participant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire()
	if err := r.allow(ctx, actor, room, Listen); err != nil {
		return nil, err
	}
	items := []Participant{}
	for _, item := range r.participants {
		if item.Actor.ServerID == actor.ServerID && item.RoomID == room {
			items = append(items, item)
		}
	}
	return items, nil
}

func (r *Rooms) expire() {
	now := r.clock()
	for id, item := range r.participants {
		if !now.Before(item.ExpiresAt) {
			delete(r.participants, id)
		}
	}
}
