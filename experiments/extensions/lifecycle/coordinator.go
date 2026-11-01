package lifecycle

import (
	"context"
	"errors"
	"sync"
)

var ErrUnavailable = errors.New("lifecycle_unavailable")

// Coordinator — один владелец экспериментальной установки в памяти.
// Он не заменяет SQL-блокировки, проверку сессий и барьер COMMIT ядра.
type Coordinator struct {
	mu       sync.Mutex
	state    string
	revision int64
	active   map[*lease]context.CancelFunc
}
type lease struct{ marker byte }

func New(state string, revision int64) (*Coordinator, error) {
	if !knownStates[state] || revision < 1 || revision > MaxRevision {
		return nil, ErrUnavailable
	}
	return &Coordinator{state: state, revision: revision, active: make(map[*lease]context.CancelFunc)}, nil
}

func (c *Coordinator) Begin(parent context.Context) (context.Context, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != "enabled" || len(c.active) >= 128 || parent.Err() != nil {
		return nil, nil, ErrUnavailable
	}
	ctx, cancel := context.WithCancel(parent)
	l := &lease{}
	c.active[l] = cancel
	var once sync.Once
	release := func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.active, l)
			c.mu.Unlock()
			cancel()
		})
	}
	return ctx, release, nil
}

// Apply сериализует закрытие входа с Begin. Активный вызов остаётся учтённым
// до release, даже после отмены: зависший worker не считается завершённым.
func (c *Coordinator) Apply(o Observation) Decision {
	c.mu.Lock()
	defer c.mu.Unlock()
	o.State, o.Revision, o.Active = c.state, c.revision, len(c.active)
	d := Decide(o)
	if d.Code != "OK" {
		return d
	}
	c.state, c.revision = d.State, d.Revision
	if d.CancelActive {
		for _, cancel := range c.active {
			cancel()
		}
	}
	return d
}
