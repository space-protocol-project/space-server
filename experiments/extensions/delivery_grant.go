package extensions

import (
	"bytes"
	"errors"
	"math"
	"sync"
)

var errDeliveryRevoked = errors.New("delivery_permission_revoked")

// Прототип: один объект на разрешение и транспорт. Все callbacks доверенные,
// ограничены deadline и не вызывают методы этого объекта повторно.
type deliveryGrant struct {
	mu            sync.Mutex
	generation    uint64
	active, clean bool
	write         func([]byte) error
	disconnect    func() error
}

type preparedDelivery struct {
	owner      *deliveryGrant
	generation uint64
	body       []byte
}

func newDeliveryGrant(write func([]byte) error, disconnect func() error) *deliveryGrant {
	return &deliveryGrant{generation: 1, active: true, write: write, disconnect: disconnect}
}

func (g *deliveryGrant) prepare(body []byte) (preparedDelivery, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.active || len(body) > 65536 {
		return preparedDelivery{}, errDeliveryRevoked
	}
	return preparedDelivery{g, g.generation, bytes.Clone(body)}, nil
}

func (g *deliveryGrant) deliver(p preparedDelivery) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.active || p.owner != g || p.generation != g.generation {
		return errDeliveryRevoked
	}
	// Проверка поколения и завершение записи — одна критическая секция.
	err := g.write(p.body)
	if err != nil {
		g.active = false
	}
	return err
}

func (g *deliveryGrant) revoke() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active = false
	if g.clean {
		return nil
	}
	// Даже при ошибке отключения новые ответы запрещены. Повтор отзыва
	// повторяет отключение; новое поколение до его успеха не выдаётся.
	err := g.disconnect()
	g.clean = err == nil
	return err
}

func (g *deliveryGrant) renew(write func([]byte) error, disconnect func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active || !g.clean || g.generation == math.MaxUint64 {
		return errDeliveryRevoked
	}
	g.generation++
	g.active, g.clean = true, false
	g.write, g.disconnect = write, disconnect
	return nil
}
