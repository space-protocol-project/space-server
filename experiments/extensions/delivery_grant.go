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

// Снимок выдаётся доверенному исполнителю до начала вычисления, не гостю.
type deliveryCall struct {
	owner      *deliveryGrant
	generation uint64
}

func newDeliveryGrant(write func([]byte) error, disconnect func() error) *deliveryGrant {
	return &deliveryGrant{generation: 1, active: true, write: write, disconnect: disconnect}
}

func (g *deliveryGrant) begin() (deliveryCall, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.active {
		return deliveryCall{}, errDeliveryRevoked
	}
	return deliveryCall{g, g.generation}, nil
}

func (g *deliveryGrant) prepare(call deliveryCall, body []byte) (preparedDelivery, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.active || call.owner != g || call.generation != g.generation || len(body) > 65536 {
		return preparedDelivery{}, errDeliveryRevoked
	}
	return preparedDelivery{g, g.generation, bytes.Clone(body)}, nil
}

// Вычисление не удерживает mutex: отзыв не ждёт его завершения. Этот адаптер
// не разрешает побочные эффекты: транзакционные команды требуют COMMIT-барьера.
func (g *deliveryGrant) invoke(compute func() ([]byte, error)) error {
	call, err := g.begin()
	if err != nil {
		return err
	}
	body, err := compute()
	if err != nil {
		return err
	}
	prepared, err := g.prepare(call, body)
	if err != nil {
		return err
	}
	return g.deliver(prepared)
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
