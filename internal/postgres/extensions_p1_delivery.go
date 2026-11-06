package postgres

import (
	"context"
	"time"

	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	"google.golang.org/grpc/codes"
)

type extensionDeliveryKey struct{}

type extensionDeliverySnapshot struct {
	principal                demo.Principal
	installation, generation string
	revision                 int64
	write                    bool
}

// Снимок берётся из тех же источников авторизации, что и проверка COMMIT.
// Транзакция не удерживается во время WASM и не охватывает сетевую отправку.
func (s *ExtensionP1ManagedService) deliverySnapshot(ctx context.Context, instance string, write bool) (extensionDeliverySnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	p := extensionDeliverySnapshot{installation: s.control.instances[instance], write: write}
	tx, err := s.control.store.pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	p.principal, err = s.control.store.extensionP1PrincipalTx(ctx, tx, s.control.channels[instance], instance, write)
	if err != nil {
		return p, err
	}
	var state string
	err = tx.QueryRow(ctx, `SELECT generation,revision,state FROM extension_p1_installations WHERE installation=$1 FOR SHARE`, p.installation).Scan(&p.generation, &p.revision, &state)
	if err != nil {
		return p, err
	}
	if p.generation != s.control.generation || state != "enabled" {
		return p, mutationError(codes.FailedPrecondition, "lifecycle_fenced")
	}
	return p, tx.Commit(ctx)
}

func (s *ExtensionP1ManagedService) checkDelivery(ctx context.Context, instance string) error {
	before, ok := ctx.Value(extensionDeliveryKey{}).(extensionDeliverySnapshot)
	if !ok || before.principal.Instance != instance {
		return denied()
	}
	after, err := s.deliverySnapshot(ctx, instance, before.write)
	if err != nil {
		return err
	}
	if after.principal != before.principal {
		return denied()
	}
	if after != before {
		return mutationError(codes.FailedPrecondition, "delivery_snapshot_changed")
	}
	return nil
}
